package ship

import (
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/steps"
)

func TestSummaryTapTakesOutTheSummaryLineSplitAnywhere(t *testing.T) {
	want := Summary{Version: "flocc 0.4.0", Login: "none", LoginPending: true}
	input := "setup output\r\nflocc: ok\r\n\r\n" + want.Line() + "\r\nPaste code here > "
	var out strings.Builder
	tap := newSummaryTap(&out)
	for i := 0; i < len(input); i++ {
		tap.Write([]byte{input[i]})
	}
	tap.Flush()
	if out.String() != "setup output\r\nflocc: ok\r\n\r\nPaste code here > " {
		t.Fatalf("output %q", out.String())
	}
	if got, ok := tap.Summary(); !ok || got.Login != "none" || !got.LoginPending {
		t.Fatalf("%+v %v", got, ok)
	}
}

func TestSummaryTapPassesAPromptAtOnce(t *testing.T) {
	var out strings.Builder
	tap := newSummaryTap(&out)
	tap.Write([]byte("Paste code here > "))
	if out.String() != "Paste code here > " {
		t.Fatalf("the prompt was held back: %q", out.String())
	}
}

func TestSummaryTapKeepsALineThatOnlyLooksLikeTheMarker(t *testing.T) {
	in := steps.SummaryMarker + "{not json\nflocc-sum is not it either\n"
	var out strings.Builder
	tap := newSummaryTap(&out)
	tap.Write([]byte(in))
	tap.Flush()
	if out.String() != in {
		t.Fatalf("output %q", out.String())
	}
	if _, ok := tap.Summary(); ok {
		t.Fatal("a corrupt line must not count as a summary")
	}
}

func TestSummaryTapFlushesAHeldPartialLine(t *testing.T) {
	var out strings.Builder
	tap := newSummaryTap(&out)
	tap.Write([]byte("flocc-sum"))
	if out.Len() != 0 {
		t.Fatalf("held bytes should wait for the rest of the marker: %q", out.String())
	}
	tap.Flush()
	if out.String() != "flocc-sum" {
		t.Fatalf("%q", out.String())
	}
}
