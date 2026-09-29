package policy

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

func TestIntegralQuantity(test *testing.T) {
	for _, testcase := range []struct {
		quantity string
		expected int
		valid    bool
	}{
		{"0", 0, true},
		{"2", 2, true},
		{"2000m", 2, true},
		{"2.0", 2, true},
		{"1500m", 0, false},
		{"-1", 0, false},
		{"9223372036854775808", 0, false},
		{"1e30", 0, false},
	} {
		test.Run(testcase.quantity, func(test *testing.T) {
			actual, valid := IntegralQuantity(resource.MustParse(testcase.quantity))
			if actual != testcase.expected || valid != testcase.valid {
				test.Fatalf("got (%d, %t), want (%d, %t)", actual, valid, testcase.expected, testcase.valid)
			}
		})
	}
}
