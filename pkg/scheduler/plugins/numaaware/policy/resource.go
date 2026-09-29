package policy

import (
	"math"

	"k8s.io/apimachinery/pkg/api/resource"
)

func IntegralQuantity(quantity resource.Quantity) (int, bool) {
	if quantity.Sign() < 0 || quantity.CmpInt64(int64(math.MaxInt)) > 0 {
		return 0, false
	}
	value := quantity.Value()
	if quantity.Cmp(*resource.NewQuantity(value, resource.DecimalSI)) != 0 {
		return 0, false
	}
	return int(value), true
}
