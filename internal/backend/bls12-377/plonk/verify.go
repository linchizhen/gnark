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
	"errors"
	"math/big"

	"github.com/consensys/gnark-crypto/ecc/bls12-377/fr"

	fiatshamir "github.com/consensys/gnark-crypto/fiat-shamir"

	bls12_377witness "github.com/consensys/gnark/internal/backend/bls12-377/witness"
)

var (
	errLhsNeqRhs = errors.New("polynomial equality doesn't hold")
)

// VerifyRaw verifies a PLONK proof
func VerifyRaw(proof *ProofRaw, publicData *PublicRaw, publicWitness bls12_377witness.Witness) error {

	// create a transcript manager to apply Fiat Shamir and get the challenges
	fs := fiatshamir.NewTranscript(fiatshamir.SHA256, "gamma", "alpha", "zeta")
	err := fs.Bind("gamma", proof.CommitmentsLROZH[0].Marshal())
	if err != nil {
		return err
	}
	err = fs.Bind("gamma", proof.CommitmentsLROZH[1].Marshal())
	if err != nil {
		return err
	}
	err = fs.Bind("gamma", proof.CommitmentsLROZH[2].Marshal())
	if err != nil {
		return err
	}
	bgamma, err := fs.ComputeChallenge("gamma")
	if err != nil {
		return err
	}
	var gamma fr.Element
	gamma.SetBytes(bgamma)

	err = fs.Bind("alpha", proof.CommitmentsLROZH[3].Marshal())
	if err != nil {
		return err
	}
	balpha, err := fs.ComputeChallenge("alpha")
	if err != nil {
		return err
	}
	var alpha fr.Element
	alpha.SetBytes(balpha)

	err = fs.Bind("zeta", proof.CommitmentsLROZH[4].Marshal())
	if err != nil {
		return err
	}
	err = fs.Bind("zeta", proof.CommitmentsLROZH[5].Marshal())
	if err != nil {
		return err
	}
	err = fs.Bind("zeta", proof.CommitmentsLROZH[6].Marshal())
	if err != nil {
		return err
	}
	bzeta, err := fs.ComputeChallenge("zeta")
	if err != nil {
		return err
	}
	var zeta fr.Element
	zeta.SetBytes(bzeta)

	// checks the opening proofs
	err = publicData.CommitmentScheme.BatchVerifySinglePoint(proof.CommitmentsLROZH[:], proof.BatchOpenings)
	if err != nil {
		return err
	}
	err = publicData.CommitmentScheme.Verify(proof.CommitmentsLROZH[3], proof.OpeningZShift)
	if err != nil {
		return err
	}

	// evaluation of ql, qr, qm, qo, qk at zeta
	var ql, qr, qm, qo, qk fr.Element
	ql.SetInterface(publicData.Ql.Eval(&zeta))
	qr.SetInterface(publicData.Qr.Eval(&zeta))
	qm.SetInterface(publicData.Qm.Eval(&zeta))
	qo.SetInterface(publicData.Qo.Eval(&zeta))
	qk.SetInterface(publicData.Qk.Eval(&zeta))

	// evaluation of Z=X**m-1 at zeta
	var zetaPowerM, zzeta, one fr.Element
	var bExpo big.Int
	one.SetOne()
	bExpo.SetUint64(publicData.DomainNum.Cardinality)
	zetaPowerM.Exp(zeta, &bExpo)
	zzeta.Sub(&zetaPowerM, &one)

	// complete L
	// TODO use batch inversion
	var lCompleted, den, acc, lagrange, xiLi fr.Element
	lagrange.Set(&zzeta) // L_0(zeta) = 1/m*(zeta**m-1)/(zeta-1)
	acc.SetOne()
	den.Sub(&zeta, &acc)
	lagrange.Div(&lagrange, &den).Mul(&lagrange, &publicData.DomainNum.CardinalityInv)
	for i := 0; i < len(publicWitness); i++ {

		xiLi.Mul(&lagrange, &publicWitness[i])
		lCompleted.Add(&lCompleted, &xiLi)

		// use L_i+1 = w*Li*(X-z**i)/(X-z**i+1)
		lagrange.Mul(&lagrange, &publicData.DomainNum.Generator).
			Mul(&lagrange, &den)
		acc.Mul(&acc, &publicData.DomainNum.Generator)
		den.Sub(&zeta, &acc)
		lagrange.Div(&lagrange, &den)
	}
	lCompleted.Add(&lCompleted, &proof.LROZH[0])

	var lroz [4]fr.Element
	lroz[0].Set(&lCompleted)
	lroz[1].Set(&proof.LROZH[1])
	lroz[2].Set(&proof.LROZH[2])
	lroz[3].Set(&proof.LROZH[3])

	// hFull = h1(zeta)+zeta^m*h2(zeta)+zeta^2m*h3(zeta)
	var hFull fr.Element
	hFull.Mul(&proof.LROZH[6], &zetaPowerM).
		Add(&hFull, &proof.LROZH[5]).
		Mul(&hFull, &zetaPowerM).
		Add(&hFull, &proof.LROZH[4])

	// evaluation of qlL+qrR+qmL.R+qoO+k at zeta
	var constraintInd fr.Element
	var qll, qrr, qmlr, qoo fr.Element
	qll.Mul(&ql, &lroz[0])
	qrr.Mul(&qr, &lroz[1])
	qmlr.Mul(&qm, &lroz[0]).Mul(&qmlr, &lroz[1])
	qoo.Mul(&qo, &lroz[2])
	constraintInd.Add(&qll, &qrr).
		Add(&constraintInd, &qmlr).
		Add(&constraintInd, &qoo).
		Add(&constraintInd, &qk)

	// evaluation of zu*g1*g2*g3*l-z*f1*f2*f3*l at zeta
	var constraintOrdering, sZeta, ssZeta fr.Element
	var s, f, g [3]fr.Element

	s[0].SetInterface(publicData.CS1.Eval(&zeta))
	s[1].SetInterface(publicData.CS2.Eval(&zeta))
	s[2].SetInterface(publicData.CS3.Eval(&zeta))

	g[0].Add(&lroz[0], &s[0]).Add(&g[0], &gamma) // l+s1+gamma
	g[1].Add(&lroz[1], &s[1]).Add(&g[1], &gamma) // r+s2+gamma
	g[2].Add(&lroz[2], &s[2]).Add(&g[2], &gamma) // o+s3+gamma
	g[0].Mul(&g[0], &g[1]).Mul(&g[0], &g[2])     // (l+s1+gamma)*(r+s2+gamma)*(o+s3+gamma) (zeta)

	sZeta.Mul(&publicData.Shifter[0], &zeta)
	ssZeta.Mul(&publicData.Shifter[1], &zeta)

	f[0].Add(&lroz[0], &zeta).Add(&f[0], &gamma)   // l+zeta+gamma
	f[1].Add(&lroz[1], &sZeta).Add(&f[1], &gamma)  // r+u*zeta+gamma
	f[2].Add(&lroz[2], &ssZeta).Add(&f[2], &gamma) // o+u*zeta+gamma
	f[0].Mul(&f[0], &f[1]).Mul(&f[0], &f[2])       // (l+zeta+gamma)*(r+u*zeta+gamma)*(r+u*zeta+gamma) (zeta)

	g[0].Mul(&g[0], &proof.ZShift)
	f[0].Mul(&f[0], &lroz[3])

	constraintOrdering.Sub(&g[0], &f[0])

	// evaluation of L1*(Z-1) at zeta (L1 = 1/m*[ (X**m-1)/(X-1) ])
	var startsAtOne, tmp, c fr.Element
	c.SetUint64(publicData.DomainNum.Cardinality)
	tmp.Sub(&lroz[3], &one) // Z(zeta)-1
	startsAtOne.
		Sub(&zeta, &one).
		Mul(&startsAtOne, &c).
		Inverse(&startsAtOne).     // 1/m*(zeta-1)
		Mul(&startsAtOne, &zzeta). // 1/m * (zeta**m-1)/(zeta-1)
		Mul(&startsAtOne, &tmp)    // (Z(zeta)-1)*L1(ze)

	// lhs = qlL+qrR+qmL.R+qoO+k(zeta) + alpha*(zu*g1*g2*g3*l-z*f1*f2*f3*l)(zeta) + alpha**2*L1(Z-1)(zeta)
	var lhs fr.Element
	lhs.Mul(&alpha, &startsAtOne).
		Add(&lhs, &constraintOrdering).
		Mul(&lhs, &alpha).
		Add(&lhs, &constraintInd)

	// rhs = h(zeta)(zeta**m-1)
	var rhs fr.Element
	rhs.Mul(&zzeta, &hFull)

	if !lhs.Equal(&rhs) {
		return errLhsNeqRhs
	}

	return nil

}
