package daemon

import (
	"context"
	"encoding/json"

	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/snapshot"
)

type DoctorReport struct {
	Status          string                `json:"status"`
	SnapshotVersion string                `json:"snapshot_version"`
	SnapshotHash    string                `json:"snapshot_hash"`
	Databases       int                   `json:"databases"`
	Rows            int                   `json:"rows"`
	History         int                   `json:"history"`
	Relations       int                   `json:"relations"`
	Security        security.DoctorReport `json:"security"`
}

func Doctor(ctx context.Context, dataDir string) (DoctorReport, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return DoctorReport{}, err
	}
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		return DoctorReport{}, err
	}
	defer func() { _ = client.Close() }()
	var report DoctorReport
	if err := client.Call(ctx, "doctor", nil, &report); err != nil {
		return DoctorReport{}, err
	}
	return report, nil
}

func (handler *databaseHandler) doctor(ctx context.Context) (DoctorReport, error) {
	securityReport, err := handler.security.Doctor(ctx)
	if err != nil {
		return DoctorReport{}, err
	}
	encoded, err := handler.export(ctx)
	if err != nil {
		return DoctorReport{}, err
	}
	hash, err := snapshot.CanonicalHash(encoded)
	if err != nil {
		return DoctorReport{}, err
	}
	var document struct {
		Catalog struct {
			Databases []json.RawMessage `json:"databases"`
		} `json:"catalog"`
		Rows      []json.RawMessage `json:"rows"`
		History   []json.RawMessage `json:"history"`
		Relations struct {
			Current []json.RawMessage `json:"current"`
		} `json:"relations"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		return DoctorReport{}, err
	}
	return DoctorReport{
		Status: "healthy", SnapshotVersion: snapshot.Version, SnapshotHash: hash,
		Databases: len(document.Catalog.Databases), Rows: len(document.Rows),
		History: len(document.History), Relations: len(document.Relations.Current),
		Security: securityReport,
	}, nil
}
