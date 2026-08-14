package journal

import (
	"testing"

	"github.com/gowebpki/jcs"
)

// RFC 8785 section 3.2.2 canonicalization sample. Keeping this vector in the
// repository prevents a dependency upgrade from silently changing journal
// bytes or hashes.
func TestRFC8785CanonicalizationVector(t *testing.T) {
	input := []byte(`{"numbers":[333333333.33333329,1E30,4.50,2e-3,0.000000000000000000000000001],"string":"\u20ac$\u000F\nA'B\"\\\\\"/","literals":[null,true,false]}`)
	want := `{"literals":[null,true,false],"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],"string":"€$\u000f\nA'B\"\\\\\"/"}`
	got, err := jcs.Transform(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("canonical form changed\ngot:  %s\nwant: %s", got, want)
	}
}
