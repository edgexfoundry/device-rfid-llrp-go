//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"github.com/pkg/errors"
	"io"
)

type fieldEncoder interface {
	EncodeFields(w io.Writer) error
}

type paramHeader struct {
	ParamType
	sz   uint16
	data fieldEncoder
	subs []paramHeader
}

func encodeParams(w io.Writer, headers ...paramHeader) error {
	for _, h := range headers {
		if h.ParamType.isTV() {
			if n, err := w.Write([]byte{byte(h.ParamType | 0x80)}); err != nil {
				return errors.Wrapf(err, "failed to write TV header for %v", h.ParamType)
			} else if n < 1 {
				return errors.Errorf("short write for %v: %d < 1", h.ParamType, n)
			}
		} else {
			if n, err := w.Write([]byte{
				byte(h.ParamType >> 8), byte(h.ParamType & 0xff),
				byte(h.sz >> 8), byte(h.sz & 0xff),
			}); err != nil {
				return errors.Wrap(err, "failed to write parameter header")
			} else if n < 4 {
				return errors.Errorf("short write: %d < 4", n)
			}
		}

		if err := h.data.EncodeFields(w); err != nil {
			return err
		}

		if err := encodeParams(w, h.subs...); err != nil {
			return err
		}
	}

	return nil
}
