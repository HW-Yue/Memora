package daemon

import (
	"context"

	"github.com/HW-Yue/Memora/internal/store"
)

type DoctorReport = store.Report

// Doctor asks the running daemon for a health report.
func Doctor(ctx context.Context, dataDir string) (DoctorReport, error) {
	client, err := dial(ctx, dataDir)
	if err != nil {
		return DoctorReport{}, err
	}
	defer func() { _ = client.Close() }()
	var report DoctorReport
	err = client.Call(ctx, "doctor", nil, &report)
	return report, err
}

func (h *handler) doctor(ctx context.Context) (DoctorReport, error) {
	return h.database.Doctor(ctx)
}
