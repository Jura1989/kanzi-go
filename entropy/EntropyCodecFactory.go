/*
Copyright 2011-2017 Frederic Langlet
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
you may obtain a copy of the License at

                http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package entropy

import (
	"fmt"
	kanzi "github.com/flanglet/kanzi"
	"strings"
)

const (
	NONE_TYPE    = uint32(0) // No compression
	HUFFMAN_TYPE = uint32(1) // Huffman
	FPAQ_TYPE    = uint32(2) // Fast PAQ (order 0)
	PAQ_TYPE     = uint32(3) // PAQ (stripped from many models for speed)
	RANGE_TYPE   = uint32(4) // Range
	ANS0_TYPE    = uint32(5) // Asymmetric Numerical System order 0
	CM_TYPE      = uint32(6) // Context Model
	TPAQ_TYPE    = uint32(7) // Tangelo PAQ
	ANS1_TYPE    = uint32(8) // Asymmetric Numerical System order 1
	TPAQX_TYPE   = uint32(9) // Tangelo PAQ Extra
)

func NewEntropyDecoder(ibs kanzi.InputBitStream, ctx map[string]interface{},
	entropyType uint32) (kanzi.EntropyDecoder, error) {
	switch entropyType {

	case HUFFMAN_TYPE:
		return NewHuffmanDecoder(ibs)

	case ANS0_TYPE:
		return NewANSRangeDecoder(ibs, 0)

	case ANS1_TYPE:
		return NewANSRangeDecoder(ibs, 1)

	case RANGE_TYPE:
		return NewRangeDecoder(ibs)

	case PAQ_TYPE:
		predictor, _ := NewPAQPredictor()
		return NewBinaryEntropyDecoder(ibs, predictor)

	case FPAQ_TYPE:
		predictor, _ := NewFPAQPredictor()
		return NewBinaryEntropyDecoder(ibs, predictor)

	case CM_TYPE:
		predictor, _ := NewCMPredictor()
		return NewBinaryEntropyDecoder(ibs, predictor)

	case TPAQ_TYPE:
		predictor, _ := NewTPAQPredictor(&ctx)
		return NewBinaryEntropyDecoder(ibs, predictor)

	case TPAQX_TYPE:
		ctx["extra"] = true
		predictor, _ := NewTPAQPredictor(&ctx)
		return NewBinaryEntropyDecoder(ibs, predictor)

	case NONE_TYPE:
		return NewNullEntropyDecoder(ibs)

	default:
		return nil, fmt.Errorf("Unsupported entropy codec type: '%c'", entropyType)
	}
}

func NewEntropyEncoder(obs kanzi.OutputBitStream, ctx map[string]interface{},
	entropyType uint32) (kanzi.EntropyEncoder, error) {
	switch entropyType {

	case HUFFMAN_TYPE:
		return NewHuffmanEncoder(obs)

	case ANS0_TYPE:
		return NewANSRangeEncoder(obs, 0)

	case ANS1_TYPE:
		return NewANSRangeEncoder(obs, 1)

	case RANGE_TYPE:
		return NewRangeEncoder(obs)

	case PAQ_TYPE:
		predictor, _ := NewPAQPredictor()
		return NewBinaryEntropyEncoder(obs, predictor)

	case FPAQ_TYPE:
		predictor, _ := NewFPAQPredictor()
		return NewBinaryEntropyEncoder(obs, predictor)

	case CM_TYPE:
		predictor, _ := NewCMPredictor()
		return NewBinaryEntropyEncoder(obs, predictor)

	case TPAQ_TYPE:
		predictor, _ := NewTPAQPredictor(&ctx)
		return NewBinaryEntropyEncoder(obs, predictor)

	case TPAQX_TYPE:
		ctx["extra"] = true
		predictor, _ := NewTPAQPredictor(&ctx)
		return NewBinaryEntropyEncoder(obs, predictor)

	case NONE_TYPE:
		return NewNullEntropyEncoder(obs)

	default:
		return nil, fmt.Errorf("Unsupported entropy codec type: '%c'", entropyType)
	}
}

func GetName(entropyType uint32) string {
	switch entropyType {

	case HUFFMAN_TYPE:
		return "HUFFMAN"

	case ANS0_TYPE:
		return "ANS0"

	case ANS1_TYPE:
		return "ANS1"

	case RANGE_TYPE:
		return "RANGE"

	case PAQ_TYPE:
		return "PAQ"

	case FPAQ_TYPE:
		return "FPAQ"

	case CM_TYPE:
		return "CM"

	case TPAQ_TYPE:
		return "TPAQ"

	case TPAQX_TYPE:
		return "TPAQX"

	case NONE_TYPE:
		return "NONE"

	default:
		panic(fmt.Errorf("Unsupported entropy codec type: '%c'", entropyType))
	}
}

func GetType(entropyName string) uint32 {
	switch strings.ToUpper(entropyName) {

	case "HUFFMAN":
		return HUFFMAN_TYPE

	case "ANS0":
		return ANS0_TYPE

	case "ANS1":
		return ANS1_TYPE

	case "RANGE":
		return RANGE_TYPE

	case "PAQ":
		return PAQ_TYPE

	case "FPAQ":
		return FPAQ_TYPE

	case "CM":
		return CM_TYPE

	case "TPAQ":
		return TPAQ_TYPE

	case "NONE":
		return NONE_TYPE

	default:
		panic(fmt.Errorf("Unsupported entropy codec type: '%s'", entropyName))
	}
}
