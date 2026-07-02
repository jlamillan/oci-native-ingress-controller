package logging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	ocilogging "github.com/oracle/oci-go-sdk/v65/logging"
	"github.com/oracle/oci-native-ingress-controller/pkg/oci/client"
	"github.com/oracle/oci-native-ingress-controller/pkg/util"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const (
	loadBalancerServiceName = "loadbalancer"
	logDisplayNameMaxLength = 255
)

type Client struct {
	LoggingClient client.LoggingInterface
	Mu            sync.Mutex
}

type logConfig struct {
	category               string
	desiredLogGroupID      string
	managedLogIDAnnotation string
	appliedGroupAnnotation string
}

func New(loggingClient *ocilogging.LoggingManagementClient) *Client {
	return &Client{
		LoggingClient: client.NewLoggingClient(loggingClient),
	}
}

func (c Client) EnsureLoadBalancerLogs(ctx context.Context, kubeClient kubernetes.Interface, ic *networkingv1.IngressClass, lbID string) error {
	for _, cfg := range []logConfig{
		{
			category:               util.LoadBalancerAccessLogCategory,
			desiredLogGroupID:      util.GetIngressClassAccessLogGroupId(ic),
			managedLogIDAnnotation: util.IngressClassAccessLogIdAnnotation,
			appliedGroupAnnotation: util.IngressClassAccessLogAppliedGroupIdAnnotation,
		},
		{
			category:               util.LoadBalancerErrorLogCategory,
			desiredLogGroupID:      util.GetIngressClassErrorLogGroupId(ic),
			managedLogIDAnnotation: util.IngressClassErrorLogIdAnnotation,
			appliedGroupAnnotation: util.IngressClassErrorLogAppliedGroupIdAnnotation,
		},
	} {
		if err := c.ensureLog(ctx, kubeClient, ic, lbID, cfg); err != nil {
			return err
		}
	}
	return nil
}

func (c Client) ensureLog(ctx context.Context, kubeClient kubernetes.Interface, ic *networkingv1.IngressClass, lbID string, cfg logConfig) error {
	managedLogID := strings.TrimSpace(getAnnotation(ic, cfg.managedLogIDAnnotation))
	appliedGroupID := strings.TrimSpace(getAnnotation(ic, cfg.appliedGroupAnnotation))

	if cfg.desiredLogGroupID == "" {
		if managedLogID == "" && appliedGroupID == "" {
			return nil
		}
		if err := c.disableManagedLog(ctx, managedLogID, appliedGroupID); err != nil {
			return err
		}
		return patchManagedLogAnnotations(kubeClient, ic, cfg, "", "")
	}

	if managedLogID != "" && appliedGroupID != "" {
		if appliedGroupID == cfg.desiredLogGroupID {
			log, err := c.GetLog(ctx, appliedGroupID, managedLogID)
			if err == nil && isLoadBalancerLog(log.Configuration, lbID, cfg.category) {
				if !isLogEnabled(log.IsEnabled) {
					if err := c.UpdateLogEnabled(ctx, appliedGroupID, managedLogID, log.Etag, true); err != nil {
						return err
					}
				}
				return nil
			}
			if err != nil && !util.IsServiceError(err, 404) {
				return err
			}
		} else if err := c.disableManagedLog(ctx, managedLogID, appliedGroupID); err != nil {
			return err
		}
	}

	log, err := c.FindLoadBalancerLog(ctx, cfg.desiredLogGroupID, lbID, cfg.category)
	if err != nil {
		return err
	}
	if log != nil {
		if !isLogEnabled(log.IsEnabled) {
			if err := c.UpdateLogEnabled(ctx, cfg.desiredLogGroupID, *log.Id, nil, true); err != nil {
				return err
			}
		}
		return patchManagedLogAnnotations(kubeClient, ic, cfg, *log.Id, cfg.desiredLogGroupID)
	}

	logID, err := c.CreateLoadBalancerLog(ctx, cfg.desiredLogGroupID, lbID, cfg.category, ic.Name)
	if err != nil {
		return err
	}
	return patchManagedLogAnnotations(kubeClient, ic, cfg, logID, cfg.desiredLogGroupID)
}

func (c Client) disableManagedLog(ctx context.Context, logID string, logGroupID string) error {
	if logID == "" || logGroupID == "" {
		return nil
	}
	log, err := c.GetLog(ctx, logGroupID, logID)
	if util.IsServiceError(err, 404) {
		return nil
	}
	if err != nil {
		return err
	}
	if !isLogEnabled(log.IsEnabled) {
		return nil
	}
	return c.UpdateLogEnabled(ctx, logGroupID, logID, log.Etag, false)
}

func (c Client) GetLog(ctx context.Context, logGroupID string, logID string) (*ocilogging.GetLogResponse, error) {
	resp, err := c.LoggingClient.GetLog(ctx, ocilogging.GetLogRequest{
		LogGroupId: common.String(logGroupID),
		LogId:      common.String(logID),
	})
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c Client) FindLoadBalancerLog(ctx context.Context, logGroupID string, lbID string, category string) (*ocilogging.LogSummary, error) {
	var page *string
	for {
		resp, err := c.LoggingClient.ListLogs(ctx, ocilogging.ListLogsRequest{
			LogGroupId:     common.String(logGroupID),
			LogType:        ocilogging.ListLogsLogTypeService,
			SourceService:  common.String(loadBalancerServiceName),
			SourceResource: common.String(lbID),
			Page:           page,
		})
		if err != nil {
			return nil, err
		}
		for i := range resp.Items {
			if isLoadBalancerLog(resp.Items[i].Configuration, lbID, category) {
				return &resp.Items[i], nil
			}
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			return nil, nil
		}
		page = resp.OpcNextPage
	}
}

func (c Client) CreateLoadBalancerLog(ctx context.Context, logGroupID string, lbID string, category string, ingressClassName string) (string, error) {
	displayName := buildLogDisplayName(ingressClassName, lbID, category)
	resp, err := c.LoggingClient.CreateLog(ctx, ocilogging.CreateLogRequest{
		LogGroupId: common.String(logGroupID),
		CreateLogDetails: ocilogging.CreateLogDetails{
			DisplayName: common.String(displayName),
			LogType:     ocilogging.CreateLogDetailsLogTypeService,
			IsEnabled:   common.Bool(true),
			FreeformTags: map[string]string{
				"oci-native-ingress-controller-resource": fmt.Sprintf("%s-log", category),
			},
			Configuration: &ocilogging.Configuration{
				Source: ocilogging.OciService{
					Service:  common.String(loadBalancerServiceName),
					Resource: common.String(lbID),
					Category: common.String(category),
				},
			},
		},
		OpcRetryToken: common.String(fmt.Sprintf("oci-nic-%s-%s-%s", category, hashID(lbID), hashID(logGroupID))),
	})
	if err != nil && !util.IsServiceError(err, 409) {
		return "", err
	}
	if resp.OpcWorkRequestId != nil {
		if logID, err := c.waitForWorkRequest(ctx, *resp.OpcWorkRequestId); err != nil || logID != "" {
			return logID, err
		}
	}

	log, err := c.FindLoadBalancerLog(ctx, logGroupID, lbID, category)
	if err != nil {
		return "", err
	}
	if log == nil || log.Id == nil {
		return "", fmt.Errorf("created %s log for load balancer %s but could not find the log id", category, lbID)
	}
	return *log.Id, nil
}

func (c Client) UpdateLogEnabled(ctx context.Context, logGroupID string, logID string, etag *string, enabled bool) error {
	resp, err := c.LoggingClient.UpdateLog(ctx, ocilogging.UpdateLogRequest{
		LogGroupId: common.String(logGroupID),
		LogId:      common.String(logID),
		IfMatch:    etag,
		UpdateLogDetails: ocilogging.UpdateLogDetails{
			IsEnabled: common.Bool(enabled),
		},
	})
	if err != nil {
		return err
	}
	if resp.OpcWorkRequestId != nil {
		_, err = c.waitForWorkRequest(ctx, *resp.OpcWorkRequestId)
	}
	return err
}

func (c Client) waitForWorkRequest(ctx context.Context, workRequestID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
			resp, err := c.LoggingClient.GetWorkRequest(ctx, ocilogging.GetWorkRequestRequest{
				WorkRequestId: common.String(workRequestID),
			})
			if err != nil {
				return "", err
			}
			switch resp.Status {
			case ocilogging.OperationStatusSucceeded:
				return firstWorkRequestResourceID(resp.Resources), nil
			case ocilogging.OperationStatusFailed, ocilogging.OperationStatusCanceled:
				return "", fmt.Errorf("logging work request %s finished with status %s", workRequestID, resp.Status)
			}
		}
	}
}

func firstWorkRequestResourceID(resources []ocilogging.WorkRequestResource) string {
	for _, resource := range resources {
		if resource.Identifier != nil && *resource.Identifier != "" {
			return *resource.Identifier
		}
	}
	return ""
}

func patchManagedLogAnnotations(kubeClient kubernetes.Interface, ic *networkingv1.IngressClass, cfg logConfig, logID string, logGroupID string) error {
	if err := patchManagedLogAnnotation(kubeClient, ic, cfg.managedLogIDAnnotation, logID); err != nil {
		return err
	}
	if err := patchManagedLogAnnotation(kubeClient, ic, cfg.appliedGroupAnnotation, logGroupID); err != nil {
		return err
	}
	return nil
}

func patchManagedLogAnnotation(kubeClient kubernetes.Interface, ic *networkingv1.IngressClass, annotation string, value string) error {
	if value != "" {
		err, retry := util.PatchIngressClassWithAnnotation(kubeClient, ic, annotation, value)
		if retry {
			return err
		}
		return nil
	}
	return removeIngressClassAnnotation(kubeClient, ic, annotation)
}

func removeIngressClassAnnotation(kubeClient kubernetes.Interface, ic *networkingv1.IngressClass, annotation string) error {
	patchMap := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				annotation: nil,
			},
		},
	}
	patchBytes, err := json.Marshal(patchMap)
	if err != nil {
		return err
	}
	if _, err := kubeClient.NetworkingV1().IngressClasses().Patch(context.TODO(), ic.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
		return err
	}
	return nil
}

func isLoadBalancerLog(configuration *ocilogging.Configuration, lbID string, category string) bool {
	if configuration == nil {
		return false
	}
	source, ok := configuration.Source.(ocilogging.OciService)
	if !ok {
		return false
	}
	return stringValue(source.Service) == loadBalancerServiceName &&
		stringValue(source.Resource) == lbID &&
		stringValue(source.Category) == category
}

func isLogEnabled(enabled *bool) bool {
	return enabled != nil && *enabled
}

func buildLogDisplayName(ingressClassName string, lbID string, category string) string {
	name := fmt.Sprintf("oci-nic-%s-%s-%s", ingressClassName, category, hashID(lbID))
	if len(name) <= logDisplayNameMaxLength {
		return name
	}
	suffix := fmt.Sprintf("-%s-%s", category, hashID(lbID))
	return name[:logDisplayNameMaxLength-len(suffix)] + suffix
}

func hashID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func getAnnotation(ic *networkingv1.IngressClass, annotation string) string {
	if ic == nil || ic.Annotations == nil {
		return ""
	}
	return ic.Annotations[annotation]
}
