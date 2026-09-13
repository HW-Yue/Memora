package daemon

import (
	"context"

	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/sqlstore"
)

type DoctorReport = sqlstore.Report

// Doctor asks the running daemon for a health report.
func Doctor(ctx context.Context, dataDir string) (DoctorReport, error) {
	client, err := dialDaemon(ctx, dataDir)
	if err != nil {
		return DoctorReport{}, err
	}
	defer func() { _ = client.Close() }()
	var report DoctorReport
	err = client.Call(ctx, "doctor", nil, &report)
	return report, err
}

func (handler *databaseHandler) doctor(ctx context.Context) (DoctorReport, error) {
	return handler.database.Doctor(ctx)
}

func dialDaemon(ctx context.Context, dataDir string) (*ipc.Client, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return nil, err
	}
	return ipc.Dial(ctx, path)
}
