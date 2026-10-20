// Package qr draws QR codes on a terminal.
package qr

import (
	"bytes"
	"errors"
	"io"

	"github.com/mdp/qrterminal/v4"
)

// Write draws text as a QR code on w. Every line ends in \r\n: ssh -t holds the terminal in raw
// mode, where \n alone does not return the cursor to column 0.
func Write(w io.Writer, text string) (err error) {
	// qrterminal ignores the encoder's error and then dereferences the nil result.
	defer func() {
		if recover() != nil {
			err = errors.New("qr: the text does not fit in a QR code")
		}
	}()
	var b bytes.Buffer
	qrterminal.GenerateHalfBlock(text, qrterminal.L, &b)
	_, err = w.Write(bytes.ReplaceAll(b.Bytes(), []byte("\n"), []byte("\r\n")))
	return err
}
