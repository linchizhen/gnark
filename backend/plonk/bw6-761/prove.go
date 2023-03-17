// Copyright 2020 ConsenSys Software Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Code generated by gnark DO NOT EDIT

package plonk

import (
	"crypto/sha256"
	"math/big"
	"runtime"
	"sync"
	"time"

	"github.com/consensys/gnark/backend/witness"

	"github.com/consensys/gnark-crypto/ecc/bw6-761/fr"

	curve "github.com/consensys/gnark-crypto/ecc/bw6-761"

	"github.com/consensys/gnark-crypto/ecc/bw6-761/fr/kzg"

	"github.com/consensys/gnark-crypto/ecc/bw6-761/fr/fft"

	"github.com/consensys/gnark-crypto/ecc/bw6-761/fr/iop"
	"github.com/consensys/gnark/constraint/bw6-761"

	"github.com/consensys/gnark-crypto/fiat-shamir"
	"github.com/consensys/gnark/backend"
	"github.com/consensys/gnark/constraint/solver"
	"github.com/consensys/gnark/internal/utils"
	"github.com/consensys/gnark/logger"
)

type Proof struct {

	// Commitments to the solution vectors
	LRO [3]kzg.Digest

	// Commitment to Z, the permutation polynomial
	Z kzg.Digest

	// Commitments to h1, h2, h3 such that h = h1 + Xh2 + X**2h3 is the quotient polynomial
	H [3]kzg.Digest

	// PI2, the BSB22 commitment
	PI2 kzg.Digest

	// Batch opening proof of h1 + zeta*h2 + zeta**2h3, linearizedPolynomial, l, r, o, s1, s2, qCPrime
	BatchedProof kzg.BatchOpeningProof

	// Opening proof of Z at zeta*mu
	ZShiftedOpening kzg.OpeningProof
}

func Prove(spr *cs.SparseR1CS, pk *ProvingKey, fullWitness witness.Witness, opts ...backend.ProverOption) (*Proof, error) {

	log := logger.Logger().With().Str("curve", spr.CurveID().String()).Int("nbConstraints", len(spr.Constraints)).Str("backend", "plonk").Logger()

	opt, err := backend.NewProverConfig(opts...)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	// pick a hash function that will be used to derive the challenges
	hFunc := sha256.New()

	// create a transcript manager to apply Fiat Shamir
	fs := fiatshamir.NewTranscript(hFunc, "gamma", "beta", "alpha", "zeta")

	// result
	proof := &Proof{}
	lagReg := iop.Form{Basis: iop.Lagrange, Layout: iop.Regular}
	var (
		wpi2iop       *iop.Polynomial // canonical
		commitmentVal fr.Element      // TODO @Tabaie get rid of this
	)
	if spr.CommitmentInfo.Is() {
		opt.SolverOpts = append(opt.SolverOpts, solver.OverrideHint(spr.CommitmentInfo.HintID, func(_ *big.Int, ins, outs []*big.Int) error {
			pi2 := make([]fr.Element, pk.Domain[0].Cardinality)
			offset := spr.GetNbPublicVariables()
			for i := range ins {
				pi2[offset+spr.CommitmentInfo.Committed[i]].SetBigInt(ins[i])
			}
			var (
				err     error
				hashRes []fr.Element
			)
			if _, err = pi2[offset+spr.CommitmentInfo.CommitmentIndex].SetRandom(); err != nil {
				return err
			}
			pi2iop := iop.NewPolynomial(&pi2, lagReg)
			wpi2iop = pi2iop.ShallowClone()
			wpi2iop.ToCanonical(&pk.Domain[0]).ToRegular()
			if proof.PI2, err = kzg.Commit(wpi2iop.Coefficients(), pk.Vk.KZGSRS); err != nil {
				return err
			}
			if hashRes, err = fr.Hash(proof.PI2.Marshal(), []byte("BSB22-Plonk"), 1); err != nil {
				return err
			}
			commitmentVal = hashRes[0] // TODO @Tabaie use CommitmentIndex for this; create a new variable CommitmentConstraintIndex for other uses
			commitmentVal.BigInt(outs[0])
			return nil
		}))
	} else {
		// TODO Leaving pi2 in for testing. In the future, bypass when no commitment present
		pi2 := make([]fr.Element, pk.Domain[0].Cardinality)

		pi2iop := iop.NewPolynomial(&pi2, lagReg)
		wpi2iop = pi2iop.ShallowClone()
		wpi2iop.ToCanonical(&pk.Domain[0]).ToRegular()
	}

	// query l, r, o in Lagrange basis, not blinded
	_solution, err := spr.Solve(fullWitness, opt.SolverOpts...)
	if err != nil {
		return nil, err
	}
	solution := _solution.(*cs.SparseR1CSSolution)
	evaluationLDomainSmall := []fr.Element(solution.L)
	evaluationRDomainSmall := []fr.Element(solution.R)
	evaluationODomainSmall := []fr.Element(solution.O)

	liop := iop.NewPolynomial(&evaluationLDomainSmall, lagReg)
	riop := iop.NewPolynomial(&evaluationRDomainSmall, lagReg)
	oiop := iop.NewPolynomial(&evaluationODomainSmall, lagReg)
	wliop := liop.ShallowClone()
	wriop := riop.ShallowClone()
	woiop := oiop.ShallowClone()
	wliop.ToCanonical(&pk.Domain[0]).ToRegular()
	wriop.ToCanonical(&pk.Domain[0]).ToRegular()
	woiop.ToCanonical(&pk.Domain[0]).ToRegular()

	// Blind l, r, o before committing
	// we set the underlying slice capacity to domain[1].Cardinality to minimize mem moves.
	bwliop := wliop.Clone(int(pk.Domain[1].Cardinality)).Blind(1)
	bwriop := wriop.Clone(int(pk.Domain[1].Cardinality)).Blind(1)
	bwoiop := woiop.Clone(int(pk.Domain[1].Cardinality)).Blind(1)
	if err := commitToLRO(bwliop.Coefficients(), bwriop.Coefficients(), bwoiop.Coefficients(), proof, pk.Vk.KZGSRS); err != nil {
		return nil, err
	}

	fw, ok := fullWitness.Vector().(fr.Vector)
	if !ok {
		return nil, witness.ErrInvalidWitness
	}

	// The first challenge is derived using the public data: the commitments to the permutation,
	// the coefficients of the circuit, and the public inputs.
	// derive gamma from the Comm(blinded cl), Comm(blinded cr), Comm(blinded co)
	if err := bindPublicData(&fs, "gamma", *pk.Vk, fw[:len(spr.Public)]); err != nil {
		return nil, err
	}
	gamma, err := deriveRandomness(&fs, "gamma", &proof.LRO[0], &proof.LRO[1], &proof.LRO[2]) // TODO @Tabaie @ThomasPiellard add BSB commitment here?
	if err != nil {
		return nil, err
	}

	// Fiat Shamir this
	bbeta, err := fs.ComputeChallenge("beta")
	if err != nil {
		return nil, err
	}
	var beta fr.Element
	beta.SetBytes(bbeta)

	// compute the copy constraint's ratio
	// We copy liop, riop, oiop because they are fft'ed in the process.
	// We could have not copied them at the cost of doing one more bit reverse
	// per poly...
	ziop, err := iop.BuildRatioCopyConstraint(
		[]*iop.Polynomial{
			liop.Clone(),
			riop.Clone(),
			oiop.Clone(),
		},
		pk.trace.S,
		beta,
		gamma,
		iop.Form{Basis: iop.Canonical, Layout: iop.Regular},
		&pk.Domain[0],
	)
	if err != nil {
		return proof, err
	}

	// commit to the blinded version of z
	bwziop := ziop // iop.NewWrappedPolynomial(&ziop)
	bwziop.Blind(2)
	proof.Z, err = kzg.Commit(bwziop.Coefficients(), pk.Vk.KZGSRS, runtime.NumCPU()*2)
	if err != nil {
		return proof, err
	}

	// derive alpha from the Comm(l), Comm(r), Comm(o), Com(Z)
	alpha, err := deriveRandomness(&fs, "alpha", &proof.Z)
	if err != nil {
		return proof, err
	}

	// compute qk in canonical basis, completed with the public inputs
	// We copy the coeffs of qk to pk is not mutated
	lqkcoef := pk.lQk.Coefficients()
	qkCompletedCanonical := make([]fr.Element, len(lqkcoef))
	copy(qkCompletedCanonical, lqkcoef)
	copy(qkCompletedCanonical, fw[:len(spr.Public)])
	if spr.CommitmentInfo.Is() {
		qkCompletedCanonical[spr.GetNbPublicVariables()+spr.CommitmentInfo.CommitmentIndex] = commitmentVal
	}
	pk.Domain[0].FFTInverse(qkCompletedCanonical, fft.DIF)
	fft.BitReverse(qkCompletedCanonical)

	// l, r, o are already blinded
	bwliop.ToLagrangeCoset(&pk.Domain[1])
	bwriop.ToLagrangeCoset(&pk.Domain[1])
	bwoiop.ToLagrangeCoset(&pk.Domain[1])
	pi2iop := wpi2iop.Clone(int(pk.Domain[1].Cardinality)).ToLagrangeCoset(&pk.Domain[1]) // lagrange coset form

	// we don't mutate so no need to clone the coefficients from the proving key.
	canReg := iop.Form{Basis: iop.Canonical, Layout: iop.Regular}
	lcqk := iop.NewPolynomial(&qkCompletedCanonical, canReg)
	lcqk.ToLagrangeCoset(&pk.Domain[1])

	// storing Id
	id := make([]fr.Element, pk.Domain[1].Cardinality)
	id[1].SetOne()
	widiop := iop.NewPolynomial(&id, canReg)
	widiop.ToLagrangeCoset(&pk.Domain[1])

	// Store z(g*x), without reallocating a slice
	bwsziop := bwziop.ShallowClone().Shift(1)
	bwsziop.ToLagrangeCoset(&pk.Domain[1])

	// L_{g^{0}}
	cap := pk.Domain[1].Cardinality
	if cap < pk.Domain[0].Cardinality {
		cap = pk.Domain[0].Cardinality // sanity check
	}
	lone := make([]fr.Element, pk.Domain[0].Cardinality, cap)
	lone[0].SetOne()
	loneiop := iop.NewPolynomial(&lone, lagReg)
	wloneiop := loneiop.ToCanonical(&pk.Domain[0]).
		ToRegular().
		ToLagrangeCoset(&pk.Domain[1])

	// Full capture using latest gnark crypto...
	fic := func(fql, fqr, fqm, fqo, fqk, fqCPrime, l, r, o, pi2 fr.Element) fr.Element { // TODO @Tabaie make use of the fact that qCPrime is a selector: sparse and binary

		var ic, tmp fr.Element

		ic.Mul(&fql, &l)
		tmp.Mul(&fqr, &r)
		ic.Add(&ic, &tmp)
		tmp.Mul(&fqm, &l).Mul(&tmp, &r)
		ic.Add(&ic, &tmp)
		tmp.Mul(&fqo, &o)
		ic.Add(&ic, &tmp).Add(&ic, &fqk)
		tmp.Mul(&fqCPrime, &pi2)
		ic.Add(&ic, &tmp)

		return ic
	}

	fo := func(l, r, o, fid, fs1, fs2, fs3, fz, fzs fr.Element) fr.Element {
		var uu fr.Element
		u := pk.Domain[0].FrMultiplicativeGen
		uu.Mul(&u, &u)

		var a, b, tmp fr.Element
		a.Mul(&beta, &fid).Add(&a, &l).Add(&a, &gamma)
		tmp.Mul(&beta, &u).Mul(&tmp, &fid).Add(&tmp, &r).Add(&tmp, &gamma)
		a.Mul(&a, &tmp)
		tmp.Mul(&beta, &uu).Mul(&tmp, &fid).Add(&tmp, &o).Add(&tmp, &gamma)
		a.Mul(&a, &tmp).Mul(&a, &fz)

		b.Mul(&beta, &fs1).Add(&b, &l).Add(&b, &gamma)
		tmp.Mul(&beta, &fs2).Add(&tmp, &r).Add(&tmp, &gamma)
		b.Mul(&b, &tmp)
		tmp.Mul(&beta, &fs3).Add(&tmp, &o).Add(&tmp, &gamma)
		b.Mul(&b, &tmp).Mul(&b, &fzs)

		b.Sub(&b, &a)

		return b
	}

	fone := func(fz, flone fr.Element) fr.Element {
		one := fr.One()
		one.Sub(&fz, &one).Mul(&one, &flone)
		return one
	}

	// 0 , 1 , 2, 3 , 4 , 5 , 6 , 7, 8 , 9  , 10, 11, 12, 13, 14,   15   , 16
	// l , r , o, id, s1, s2, s3, z, zs, PI2, ql, qr, qm, qo, qk, qCPrime, lone
	fm := func(x ...fr.Element) fr.Element {

		a := fic(x[10], x[11], x[12], x[13], x[14], x[15], x[0], x[1], x[2], x[9])
		b := fo(x[0], x[1], x[2], x[3], x[4], x[5], x[6], x[7], x[8])
		c := fone(x[7], x[16])

		c.Mul(&c, &alpha).Add(&c, &b).Mul(&c, &alpha).Add(&c, &a)

		return c
	}
	systemEvaluation, err := iop.Evaluate(fm, iop.Form{Basis: iop.LagrangeCoset, Layout: iop.BitReverse},
		bwliop,
		bwriop,
		bwoiop,
		widiop,
		pk.lcS1,
		pk.lcS2,
		pk.lcS3,
		bwziop,
		bwsziop,
		pi2iop,
		pk.lcQl,
		pk.lcQr,
		pk.lcQm,
		pk.lcQo,
		lcqk,
		pk.lcQcp,
		wloneiop,
	)
	if err != nil {
		return nil, err
	}
	h, err := iop.DivideByXMinusOne(systemEvaluation, [2]*fft.Domain{&pk.Domain[0], &pk.Domain[1]}) // TODO Rename to DivideByXNMinusOne or DivideByVanishingPoly etc
	if err != nil {
		return nil, err
	}

	// compute kzg commitments of h1, h2 and h3
	if err := commitToQuotient(
		h.Coefficients()[:pk.Domain[0].Cardinality+2],
		h.Coefficients()[pk.Domain[0].Cardinality+2:2*(pk.Domain[0].Cardinality+2)],
		h.Coefficients()[2*(pk.Domain[0].Cardinality+2):3*(pk.Domain[0].Cardinality+2)],
		proof, pk.Vk.KZGSRS); err != nil {
		return nil, err
	}

	// derive zeta
	zeta, err := deriveRandomness(&fs, "zeta", &proof.H[0], &proof.H[1], &proof.H[2])
	if err != nil {
		return nil, err
	}

	// compute evaluations of (blinded version of) l, r, o, z, qCPrime at zeta
	var blzeta, brzeta, bozeta, qcpzeta fr.Element

	var wgEvals sync.WaitGroup
	wgEvals.Add(4)
	evalAtZeta := func(poly *iop.Polynomial, res *fr.Element) {
		poly.ToCanonical(&pk.Domain[1]).ToRegular()
		*res = poly.Evaluate(zeta)
		wgEvals.Done()
	}
	go evalAtZeta(bwliop, &blzeta)
	go evalAtZeta(bwriop, &brzeta)
	go evalAtZeta(bwoiop, &bozeta)
	go func() {
		qcpzeta = pk.trace.Qcp.Evaluate(zeta)
		wgEvals.Done()
	}()

	// open blinded Z at zeta*z
	bwziop.ToCanonical(&pk.Domain[1]).ToRegular()
	var zetaShifted fr.Element
	zetaShifted.Mul(&zeta, &pk.Vk.Generator)
	proof.ZShiftedOpening, err = kzg.Open(
		bwziop.Coefficients()[:bwziop.BlindedSize()],
		zetaShifted,
		pk.Vk.KZGSRS,
	)
	if err != nil {
		return nil, err
	}

	// blinded z evaluated at u*zeta
	bzuzeta := proof.ZShiftedOpening.ClaimedValue

	var (
		linearizedPolynomialCanonical []fr.Element
		linearizedPolynomialDigest    curve.G1Affine
		errLPoly                      error
	)

	wgEvals.Wait() // wait for the evaluations

	// compute the linearization polynomial r at zeta
	// (goal: save committing separately to z, ql, qr, qm, qo, k
	linearizedPolynomialCanonical = computeLinearizedPolynomial(
		blzeta,
		brzeta,
		bozeta,
		alpha,
		beta,
		gamma,
		zeta,
		bzuzeta,
		qcpzeta,
		bwziop.Coefficients()[:bwziop.BlindedSize()],
		wpi2iop.Coefficients(),
		pk,
	)

	// TODO this commitment is only necessary to derive the challenge, we should
	// be able to avoid doing it and get the challenge in another way
	linearizedPolynomialDigest, errLPoly = kzg.Commit(linearizedPolynomialCanonical, pk.Vk.KZGSRS)

	// foldedHDigest = Comm(h1) + ζᵐ⁺²*Comm(h2) + ζ²⁽ᵐ⁺²⁾*Comm(h3)
	var bZetaPowerm, bSize big.Int
	bSize.SetUint64(pk.Domain[0].Cardinality + 2) // +2 because of the masking (h of degree 3(n+2)-1)
	var zetaPowerm fr.Element
	zetaPowerm.Exp(zeta, &bSize)
	zetaPowerm.BigInt(&bZetaPowerm)
	foldedHDigest := proof.H[2]
	foldedHDigest.ScalarMultiplication(&foldedHDigest, &bZetaPowerm)
	foldedHDigest.Add(&foldedHDigest, &proof.H[1])                   // ζᵐ⁺²*Comm(h3)
	foldedHDigest.ScalarMultiplication(&foldedHDigest, &bZetaPowerm) // ζ²⁽ᵐ⁺²⁾*Comm(h3) + ζᵐ⁺²*Comm(h2)
	foldedHDigest.Add(&foldedHDigest, &proof.H[0])                   // ζ²⁽ᵐ⁺²⁾*Comm(h3) + ζᵐ⁺²*Comm(h2) + Comm(h1)

	// foldedH = h1 + ζ*h2 + ζ²*h3
	foldedH := h.Coefficients()[2*(pk.Domain[0].Cardinality+2) : 3*(pk.Domain[0].Cardinality+2)]
	h2 := h.Coefficients()[pk.Domain[0].Cardinality+2 : 2*(pk.Domain[0].Cardinality+2)]
	h1 := h.Coefficients()[:pk.Domain[0].Cardinality+2]
	utils.Parallelize(len(foldedH), func(start, end int) {
		for i := start; i < end; i++ {
			foldedH[i].Mul(&foldedH[i], &zetaPowerm) // ζᵐ⁺²*h3
			foldedH[i].Add(&foldedH[i], &h2[i])      // ζ^{m+2)*h3+h2
			foldedH[i].Mul(&foldedH[i], &zetaPowerm) // ζ²⁽ᵐ⁺²⁾*h3+h2*ζᵐ⁺²
			foldedH[i].Add(&foldedH[i], &h1[i])      // ζ^{2(m+2)*h3+ζᵐ⁺²*h2 + h1
		}
	})

	if errLPoly != nil {
		return nil, errLPoly
	}

	// Batch open the first list of polynomials
	proof.BatchedProof, err = kzg.BatchOpenSinglePoint(
		[][]fr.Element{
			foldedH,
			linearizedPolynomialCanonical,
			bwliop.Coefficients()[:bwliop.BlindedSize()],
			bwriop.Coefficients()[:bwriop.BlindedSize()],
			bwoiop.Coefficients()[:bwoiop.BlindedSize()],
			pk.trace.S1.Coefficients(),
			pk.trace.S2.Coefficients(),
			pk.trace.Qcp.Coefficients(),
		},
		[]kzg.Digest{
			foldedHDigest,
			linearizedPolynomialDigest,
			proof.LRO[0],
			proof.LRO[1],
			proof.LRO[2],
			pk.Vk.S[0],
			pk.Vk.S[1],
			pk.Vk.Qcp,
		},
		zeta,
		hFunc,
		pk.Vk.KZGSRS,
	)

	log.Debug().Dur("took", time.Since(start)).Msg("prover done")

	if err != nil {
		return nil, err
	}

	return proof, nil

}

// fills proof.LRO with kzg commits of bcl, bcr and bco
func commitToLRO(bcl, bcr, bco []fr.Element, proof *Proof, srs *kzg.SRS) error {
	n := runtime.NumCPU() / 2
	var err0, err1, err2 error
	chCommit0 := make(chan struct{}, 1)
	chCommit1 := make(chan struct{}, 1)
	go func() {
		proof.LRO[0], err0 = kzg.Commit(bcl, srs, n)
		close(chCommit0)
	}()
	go func() {
		proof.LRO[1], err1 = kzg.Commit(bcr, srs, n)
		close(chCommit1)
	}()
	if proof.LRO[2], err2 = kzg.Commit(bco, srs, n); err2 != nil {
		return err2
	}
	<-chCommit0
	<-chCommit1

	if err0 != nil {
		return err0
	}

	return err1
}

func commitToQuotient(h1, h2, h3 []fr.Element, proof *Proof, srs *kzg.SRS) error {
	n := runtime.NumCPU() / 2
	var err0, err1, err2 error
	chCommit0 := make(chan struct{}, 1)
	chCommit1 := make(chan struct{}, 1)
	go func() {
		proof.H[0], err0 = kzg.Commit(h1, srs, n)
		close(chCommit0)
	}()
	go func() {
		proof.H[1], err1 = kzg.Commit(h2, srs, n)
		close(chCommit1)
	}()
	if proof.H[2], err2 = kzg.Commit(h3, srs, n); err2 != nil {
		return err2
	}
	<-chCommit0
	<-chCommit1

	if err0 != nil {
		return err0
	}

	return err1
}

// computeLinearizedPolynomial computes the linearized polynomial in canonical basis.
// The purpose is to commit and open all in one ql, qr, qm, qo, qk.
// * lZeta, rZeta, oZeta are the evaluation of l, r, o at zeta
// * z is the permutation polynomial, zu is Z(μX), the shifted version of Z
// * pk is the proving key: the linearized polynomial is a linear combination of ql, qr, qm, qo, qk.
//
// The Linearized polynomial is:
//
// α²*L₁(ζ)*Z(X)
// + α*( (l(ζ)+β*s1(ζ)+γ)*(r(ζ)+β*s2(ζ)+γ)*Z(μζ)*s3(X) - Z(X)*(l(ζ)+β*id1(ζ)+γ)*(r(ζ)+β*id2(ζ)+γ)*(o(ζ)+β*id3(ζ)+γ))
// + l(ζ)*Ql(X) + l(ζ)r(ζ)*Qm(X) + r(ζ)*Qr(X) + o(ζ)*Qo(X) + Qk(X)
func computeLinearizedPolynomial(lZeta, rZeta, oZeta, alpha, beta, gamma, zeta, zu, qcpZeta fr.Element, blindedZCanonical []fr.Element, pi2Canonical []fr.Element, pk *ProvingKey) []fr.Element {

	// first part: individual constraints
	var rl fr.Element
	rl.Mul(&rZeta, &lZeta)

	// second part:
	// Z(μζ)(l(ζ)+β*s1(ζ)+γ)*(r(ζ)+β*s2(ζ)+γ)*β*s3(X)-Z(X)(l(ζ)+β*id1(ζ)+γ)*(r(ζ)+β*id2(ζ)+γ)*(o(ζ)+β*id3(ζ)+γ)
	var s1, s2 fr.Element
	chS1 := make(chan struct{}, 1)
	go func() {
		s1 = pk.trace.S1.Evaluate(zeta)                      // s1(ζ)
		s1.Mul(&s1, &beta).Add(&s1, &lZeta).Add(&s1, &gamma) // (l(ζ)+β*s1(ζ)+γ)
		close(chS1)
	}()
	// ps2 := iop.NewPolynomial(&pk.S2Canonical, iop.Form{Basis: iop.Canonical, Layout: iop.Regular})
	tmp := pk.trace.S2.Evaluate(zeta)                        // s2(ζ)
	tmp.Mul(&tmp, &beta).Add(&tmp, &rZeta).Add(&tmp, &gamma) // (r(ζ)+β*s2(ζ)+γ)
	<-chS1
	s1.Mul(&s1, &tmp).Mul(&s1, &zu).Mul(&s1, &beta) // (l(ζ)+β*s1(β)+γ)*(r(ζ)+β*s2(β)+γ)*β*Z(μζ)

	var uzeta, uuzeta fr.Element
	uzeta.Mul(&zeta, &pk.Vk.CosetShift)
	uuzeta.Mul(&uzeta, &pk.Vk.CosetShift)

	s2.Mul(&beta, &zeta).Add(&s2, &lZeta).Add(&s2, &gamma)      // (l(ζ)+β*ζ+γ)
	tmp.Mul(&beta, &uzeta).Add(&tmp, &rZeta).Add(&tmp, &gamma)  // (r(ζ)+β*u*ζ+γ)
	s2.Mul(&s2, &tmp)                                           // (l(ζ)+β*ζ+γ)*(r(ζ)+β*u*ζ+γ)
	tmp.Mul(&beta, &uuzeta).Add(&tmp, &oZeta).Add(&tmp, &gamma) // (o(ζ)+β*u²*ζ+γ)
	s2.Mul(&s2, &tmp)                                           // (l(ζ)+β*ζ+γ)*(r(ζ)+β*u*ζ+γ)*(o(ζ)+β*u²*ζ+γ)
	s2.Neg(&s2)                                                 // -(l(ζ)+β*ζ+γ)*(r(ζ)+β*u*ζ+γ)*(o(ζ)+β*u²*ζ+γ)

	// third part L₁(ζ)*α²*Z
	var lagrangeZeta, one, den, frNbElmt fr.Element
	one.SetOne()
	nbElmt := int64(pk.Domain[0].Cardinality)
	lagrangeZeta.Set(&zeta).
		Exp(lagrangeZeta, big.NewInt(nbElmt)).
		Sub(&lagrangeZeta, &one)
	frNbElmt.SetUint64(uint64(nbElmt))
	den.Sub(&zeta, &one).
		Inverse(&den)
	lagrangeZeta.Mul(&lagrangeZeta, &den). // L₁ = (ζⁿ⁻¹)/(ζ-1)
						Mul(&lagrangeZeta, &alpha).
						Mul(&lagrangeZeta, &alpha).
						Mul(&lagrangeZeta, &pk.Domain[0].CardinalityInv) // (1/n)*α²*L₁(ζ)

	linPol := make([]fr.Element, len(blindedZCanonical))
	copy(linPol, blindedZCanonical)

	s3canonical := pk.trace.S3.Coefficients()
	utils.Parallelize(len(linPol), func(start, end int) {

		var t0, t1 fr.Element

		for i := start; i < end; i++ {

			linPol[i].Mul(&linPol[i], &s2) // -Z(X)*(l(ζ)+β*ζ+γ)*(r(ζ)+β*u*ζ+γ)*(o(ζ)+β*u²*ζ+γ)

			if i < len(s3canonical) {

				t0.Mul(&s3canonical[i], &s1) // (l(ζ)+β*s1(ζ)+γ)*(r(ζ)+β*s2(ζ)+γ)*Z(μζ)*β*s3(X)

				linPol[i].Add(&linPol[i], &t0)
			}

			linPol[i].Mul(&linPol[i], &alpha) // α*( (l(ζ)+β*s1(ζ)+γ)*(r(ζ)+β*s2(ζ)+γ)*Z(μζ)*s3(X) - Z(X)*(l(ζ)+β*ζ+γ)*(r(ζ)+β*u*ζ+γ)*(o(ζ)+β*u²*ζ+γ))

			cql := pk.trace.Ql.Coefficients()
			cqr := pk.trace.Qr.Coefficients()
			cqm := pk.trace.Qm.Coefficients()
			cqo := pk.trace.Qo.Coefficients()
			cqk := pk.trace.Qk.Coefficients()
			if i < len(cqm) {

				t1.Mul(&cqm[i], &rl) // linPol = linPol + l(ζ)r(ζ)*Qm(X)
				t0.Mul(&cql[i], &lZeta)
				t0.Add(&t0, &t1)
				linPol[i].Add(&linPol[i], &t0) // linPol = linPol + l(ζ)*Ql(X)

				t0.Mul(&cqr[i], &rZeta)
				linPol[i].Add(&linPol[i], &t0) // linPol = linPol + r(ζ)*Qr(X)

				t0.Mul(&cqo[i], &oZeta).Add(&t0, &cqk[i])
				linPol[i].Add(&linPol[i], &t0) // linPol = linPol + o(ζ)*Qo(X) + Qk(X)

				t0.Mul(&pi2Canonical[i], &qcpZeta)
				linPol[i].Add(&linPol[i], &t0)
			}

			t0.Mul(&blindedZCanonical[i], &lagrangeZeta)
			linPol[i].Add(&linPol[i], &t0) // finish the computation
		}
	})
	return linPol
}
