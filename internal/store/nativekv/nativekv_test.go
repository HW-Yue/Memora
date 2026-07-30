package nativekv_test

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/store/nativekv"
	"github.com/HW-Yue/Memora/internal/store/storetest"
)

func TestContract(t *testing.T) {
	storetest.Run(t, nativekv.Open)
}
