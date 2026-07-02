package client

import (
	"context"

	"github.com/oracle/oci-go-sdk/v65/logging"
)

type LoggingInterface interface {
	CreateLog(ctx context.Context, request logging.CreateLogRequest) (logging.CreateLogResponse, error)
	GetLog(ctx context.Context, request logging.GetLogRequest) (logging.GetLogResponse, error)
	GetWorkRequest(ctx context.Context, request logging.GetWorkRequestRequest) (logging.GetWorkRequestResponse, error)
	ListLogs(ctx context.Context, request logging.ListLogsRequest) (logging.ListLogsResponse, error)
	UpdateLog(ctx context.Context, request logging.UpdateLogRequest) (logging.UpdateLogResponse, error)
}

type LoggingClient struct {
	loggingClient *logging.LoggingManagementClient
}

func NewLoggingClient(loggingClient *logging.LoggingManagementClient) LoggingClient {
	return LoggingClient{
		loggingClient: loggingClient,
	}
}

func (client LoggingClient) CreateLog(ctx context.Context, request logging.CreateLogRequest) (logging.CreateLogResponse, error) {
	return client.loggingClient.CreateLog(ctx, request)
}

func (client LoggingClient) GetLog(ctx context.Context, request logging.GetLogRequest) (logging.GetLogResponse, error) {
	return client.loggingClient.GetLog(ctx, request)
}

func (client LoggingClient) GetWorkRequest(ctx context.Context, request logging.GetWorkRequestRequest) (logging.GetWorkRequestResponse, error) {
	return client.loggingClient.GetWorkRequest(ctx, request)
}

func (client LoggingClient) ListLogs(ctx context.Context, request logging.ListLogsRequest) (logging.ListLogsResponse, error) {
	return client.loggingClient.ListLogs(ctx, request)
}

func (client LoggingClient) UpdateLog(ctx context.Context, request logging.UpdateLogRequest) (logging.UpdateLogResponse, error) {
	return client.loggingClient.UpdateLog(ctx, request)
}
