/*
Copyright 2017 The Kubernetes Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package provider

import (
	"context"
	"fmt"
	"time"
	"net"
  "net/http"
  "regexp"

	pmodel "github.com/prometheus/common/model"

	apierr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
	"k8s.io/metrics/pkg/apis/external_metrics"

	"sigs.k8s.io/custom-metrics-apiserver/pkg/provider"

	prom "sigs.k8s.io/prometheus-adapter/pkg/client"
	"sigs.k8s.io/prometheus-adapter/pkg/naming"
	"github.com/prometheus/client_golang/prometheus"
  "github.com/prometheus/client_golang/prometheus/promhttp"
)

type externalPrometheusProvider struct {
	promClient      prom.Client
	metricConverter MetricConverter

	seriesRegistry ExternalSeriesRegistry
}

var (
	externalMetricsFailureCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "external_metrics_query_failure_total",
			Help: "Total number of failed external metrics query attempts",
		},
		[]string{"statusCode"},
	)
)

func init() {
	mux := http.NewServeMux()

	mux.Handle("/metrics", promhttp.InstrumentMetricHandler(
		prometheus.DefaultRegisterer,
		promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{
			ErrorHandling: promhttp.PanicOnError,
		}),
	))

	// Register the metric with Prometheus.
	prometheus.MustRegister(externalMetricsFailureCounter)

	// Check if a listener is already active on port 8080
  port := ":8080"
  addr, err := net.ResolveTCPAddr("tcp", port)
  if err != nil {
    klog.Fatalf("[http] Failed to resolve address for prom-adapter on port 8080, error: %+v", err)
  }

  conn, err := net.Dial("tcp", addr.String())
  if err == nil {
    // A listener is already active; connect to it
    klog.Infof("[http] Found an active listener from prom-adapter on port %s, reusing the connection.", port)
    conn.Close() // Close the test connection
    return
  }

  // If no listener is active, create one
  listener, err := net.Listen("tcp", port)
  if err != nil {
    klog.Fatalf("[http] Failed to create listener for prom-adapter on port %s, error: %+v", port, err)
  }
  klog.Infof("[http] prom-adapter /metrics port listening on %s", listener.Addr())

  // Start serving using the listener
  go func() {
    err := http.Serve(listener, mux)
    if err != nil {
      klog.Warningf("[http] prom-adapter /metrics port error serving http: %+v", err)
    }
  }()
}


func (p *externalPrometheusProvider) GetExternalMetric(ctx context.Context, namespace string, metricSelector labels.Selector, info provider.ExternalMetricInfo) (*external_metrics.ExternalMetricValueList, error) {
	selector, found, err := p.seriesRegistry.QueryForMetric(namespace, info.Metric, metricSelector)

	if err != nil {
		klog.Errorf("unable to generate a query for the metric: %v", err)
		return nil, apierr.NewInternalError(fmt.Errorf("unable to fetch metrics"))
	}

	if !found {
		return nil, provider.NewMetricNotFoundError(p.selectGroupResource(namespace), info.Metric)
	}
	// Here is where we're making the query, need to be before here xD
	queryResults, err := p.promClient.Query(ctx, pmodel.Now(), selector)

  re := regexp.MustCompile(`\[Status Code: (\d{3})\]`)
	if err != nil {
		klog.Errorf("unable to fetch metrics from prometheus: %v", err)
    matches := re.FindStringSubmatch(err.Error())
    statusCode := "unknown"
    if len(matches) > 1 {
      statusCode = matches[1] // The captured status code
    }
		externalMetricsFailureCounter.WithLabelValues(statusCode).Inc()
		// don't leak implementation details to the user
		return nil, apierr.NewInternalError(fmt.Errorf("unable to fetch metrics"))
	}
	return p.metricConverter.Convert(info, queryResults)
}

func (p *externalPrometheusProvider) ListAllExternalMetrics() []provider.ExternalMetricInfo {
	return p.seriesRegistry.ListAllMetrics()
}

func (p *externalPrometheusProvider) selectGroupResource(namespace string) schema.GroupResource {
	if namespace == "default" {
		return naming.NsGroupResource
	}

	return schema.GroupResource{
		Group:    "",
		Resource: "",
	}
}

// NewExternalPrometheusProvider creates an ExternalMetricsProvider capable of responding to Kubernetes requests for external metric data
func NewExternalPrometheusProvider(promClient prom.Client, namers []naming.MetricNamer, updateInterval time.Duration, maxAge time.Duration) (provider.ExternalMetricsProvider, Runnable) {
	metricConverter := NewMetricConverter()
	basicLister := NewBasicMetricLister(promClient, namers, maxAge)
	periodicLister, _ := NewPeriodicMetricLister(basicLister, updateInterval)
	seriesRegistry := NewExternalSeriesRegistry(periodicLister)
	return &externalPrometheusProvider{
		promClient:      promClient,
		seriesRegistry:  seriesRegistry,
		metricConverter: metricConverter,
	}, periodicLister
}
