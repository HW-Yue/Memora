package daemon

import (
	"context"

	"github.com/HW-Yue/Memora/internal/feedback"
	"github.com/HW-Yue/Memora/internal/ipc"
)

func RecordFeedback(ctx context.Context, dataDir string, event feedback.Event) (feedback.Receipt, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return feedback.Receipt{}, err
	}
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		return feedback.Receipt{}, err
	}
	defer func() { _ = client.Close() }()
	var receipt feedback.Receipt
	err = client.Call(ctx, "feedback.record", event, &receipt)
	return receipt, err
}

func ConfirmFeedback(ctx context.Context, dataDir string, confirmation feedback.Confirmation) (feedback.ConfirmationReceipt, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return feedback.ConfirmationReceipt{}, err
	}
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		return feedback.ConfirmationReceipt{}, err
	}
	defer func() { _ = client.Close() }()
	var receipt feedback.ConfirmationReceipt
	err = client.Call(ctx, "feedback.confirm", confirmation, &receipt)
	return receipt, err
}
