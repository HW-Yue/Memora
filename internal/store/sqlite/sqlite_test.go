package sqlite

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/store/storetest"
)

func TestStoreContract(t *testing.T) {
	storetest.Run(t, Open)
}
