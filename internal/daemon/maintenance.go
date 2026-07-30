package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/reindex"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/semantichealth"
)

func SemanticHealth(ctx context.Context, dataDir string) (semantichealth.Report, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return semantichealth.Report{}, err
	}
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		return semantichealth.Report{}, err
	}
	defer func() { _ = client.Close() }()
	var report semantichealth.Report
	err = client.Call(ctx, "semantic_health.report", nil, &report)
	return report, err
}

func Maintain(ctx context.Context, dataDir string, request semantichealth.Request) (semantichealth.Receipt, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return semantichealth.Receipt{}, err
	}
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		return semantichealth.Receipt{}, err
	}
	defer func() { _ = client.Close() }()
	var receipt semantichealth.Receipt
	err = client.Call(ctx, "semantic_health.maintain", request, &receipt)
	return receipt, err
}

func (handler *databaseHandler) handleSemanticHealth(ctx context.Context, request ipc.Request) (json.RawMessage, error) {
	source, ok := handlerHealthSource(handler)
	if !ok {
		return nil, &semantichealth.Error{Code: result.CodeInternal, Message: "semantic health source is unavailable"}
	}
	service := semantichealth.New(source, handler.store)
	if request.Method == "semantic_health.report" {
		report, err := service.Report(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(report)
	}
	var maintenanceRequest semantichealth.Request
	decoder := json.NewDecoder(bytes.NewReader(request.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&maintenanceRequest); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, &semantichealth.Error{Code: result.CodeInvalidRequest, Message: "maintenance request payload is invalid"}
	}
	receipt, err := service.Maintain(ctx, maintenanceRequest)
	if err != nil {
		return nil, err
	}
	return json.Marshal(receipt)
}

type daemonHealthSource struct {
	catalog interface {
		ShowDatabases(context.Context) ([]catalog.Database, error)
	}
	rows *row.Service
}

func handlerHealthSource(handler *databaseHandler) (*daemonHealthSource, bool) {
	dictionary, ok := handler.dictionary.(interface {
		ShowDatabases(context.Context) ([]catalog.Database, error)
	})
	return &daemonHealthSource{catalog: dictionary, rows: handler.rows}, ok
}

func (source *daemonHealthSource) ShowDatabases(ctx context.Context) ([]catalog.Database, error) {
	return source.catalog.ShowDatabases(ctx)
}
func (source *daemonHealthSource) ListPage(ctx context.Context, database, table string, limit int) ([]row.Row, bool, error) {
	return source.rows.ListPage(ctx, database, table, limit)
}
func (source *daemonHealthSource) ResolveRouterPath(ctx context.Context, databaseID, path string) (router.Node, error) {
	return source.rows.ResolveRouterPath(ctx, databaseID, path)
}
func (source *daemonHealthSource) ListRouterChildren(ctx context.Context, parentID, cursor string, limit int) ([]router.Node, string, error) {
	return source.rows.ListRouterChildren(ctx, parentID, cursor, limit)
}
func (source *daemonHealthSource) ListRouterLeaf(ctx context.Context, leafID string, limit int) ([]router.Locator, bool, error) {
	return source.rows.ListRouterLeaf(ctx, leafID, limit)
}
func (source *daemonHealthSource) ListReindexTasks(ctx context.Context) ([]reindex.Task, error) {
	return source.rows.ListReindexTasks(ctx)
}
func (source *daemonHealthSource) RetryReindex(ctx context.Context, databaseID, tableID, rowID string) error {
	return source.rows.RetryReindex(ctx, databaseID, tableID, rowID)
}
