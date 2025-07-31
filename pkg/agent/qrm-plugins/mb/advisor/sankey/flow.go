/*
Copyright 2022 The Katalyst Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sankey

import (
	"github.com/pkg/errors"
	"gonum.org/v1/gonum/mat"
)

type DomainFlower interface {
	// InvertFlow attributes incoming MB to outgoing traffics
	InvertFlow(localRatio []float64, incoming []int) (outgoing []int, err error)
}

// fillUpMatrixSlice generates 1-D shape of nxn matrix M, based on outgoingLocalRatio specifically
// which satisfies:
// M[i][i] = outgoingLocalRatio[i],
// M[i][j] = (1-outgoingLocalRatio[i])/(n-1)
func fillUpMatrixSlice(outgoingLocalRatio []float64) []float64 {
	dim := len(outgoingLocalRatio)
	m := make([]float64, dim*dim)
	for i, r := range outgoingLocalRatio {
		avgRemoteRatio := (1 - r) / float64(dim-1)
		for j := 0; j < dim; j++ {
			index1D := j + i*dim
			m[index1D] = avgRemoteRatio
			if i == j {
				m[index1D] = r
			}
		}
	}
	return m
}

func get_matrix_inv(data []float64, dim int) (mat.Matrix, error) {
	M := mat.NewDense(dim, dim, data)
	var MInv mat.Dense
	if err := MInv.Inverse(M); err != nil {
		return nil, errors.Wrap(err, "failed to get matrix invert")
	}
	return &MInv, nil
}

func get_coefficient_inv(outgoingLocalRatio []float64) (mat.Matrix, error) {
	dim := len(outgoingLocalRatio)
	data := fillUpMatrixSlice(outgoingLocalRatio)
	return get_matrix_inv(data, dim)
}

func to1DMatrix(v []int) mat.Matrix {
	dim := len(v)
	rawB := make([]float64, dim)
	for i, v := range v {
		rawB[i] = float64(v)
	}
	return mat.NewDense(1, dim, rawB)
}

func from1DMatrix(m *mat.Dense) []int {
	_, dim := m.Dims()
	result := make([]int, dim)
	for i, a := range m.RawRowView(0) {
		result[i] = int(a)
	}
	return result
}

type domainFlower struct{}

func (d domainFlower) InvertFlow(localRatio []float64, incoming []int) (outgoing []int, err error) {
	// given A*M=B, to get A = B*invM
	invM, err := get_coefficient_inv(localRatio)
	if err != nil {
		return nil, errors.Wrap(err, "failed to invert flow")
	}

	B := to1DMatrix(incoming)

	var A mat.Dense
	A.Mul(B, invM)

	outgoing = from1DMatrix(&A)
	return
}

type zeroOutDomainFlower struct {
	inner DomainFlower
}

func (s *zeroOutDomainFlower) InvertFlow(localRatio []float64, incoming []int) (outgoing []int, err error) {
	outgoing, err = s.inner.InvertFlow(localRatio, incoming)
	if err != nil {
		return nil, errors.Wrap(err, "failed to delegate to inner")
	}

	for i, v := range outgoing {
		if v < 0 {
			outgoing[i] = 0
		}
	}

	return outgoing, nil
}

func New() DomainFlower {
	return &zeroOutDomainFlower{
		inner: &domainFlower{},
	}
}
