package azure_monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/azure/auth"
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/inputs"
)

type AzureMonitorError struct {
	message string
}

func (e *AzureMonitorError) Error() string {
	return e.message
}

type AzureMonitorResponseValueName struct {
	LocalizedValue string
	Value          string
}

type AzureMonitorResponseTimeSeriesDatum struct {
	Average   float64
	TimeStamp string
}

type AzureMonitorResponseTimeSeries struct {
	Data           []AzureMonitorResponseTimeSeriesDatum
	MetadataValues []map[string]interface{}
}

type AzureMonitorResponseValue struct {
	DisplayDescription string
	ErrorCode          string
	Id                 string
	Name               AzureMonitorResponseValueName
	// ResourceGroup string
	TimeSeries []AzureMonitorResponseTimeSeries
	Type       string
	Unit       string
}

type AzureMonitorResponse struct {
	Cost           float64
	Interval       string
	Namespace      string
	ResourceRegion string
	Timespan       string
	Value          []AzureMonitorResponseValue
}

func parseResponse(resp *http.Response) (AzureMonitorResponse, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return AzureMonitorResponse{}, err
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return AzureMonitorResponse{}, &AzureMonitorError{message: fmt.Sprintf("Azure monitor request returned error. Status %v:\n%v", resp.StatusCode, string(body))}
	}

	var response AzureMonitorResponse
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	err = decoder.Decode(&response)
	if err != nil {
		return AzureMonitorResponse{}, err
	}

	return response, nil
}

type AzureMonitor struct {
	ResourceId string              `toml:"resource_id"`
	authorizer autorest.Authorizer `toml:"-"`
	Log        telegraf.Logger     `toml:"-"`
}

func (a *AzureMonitor) Description() string {
	return "Gather Azure monitor metrics"
}

func (a *AzureMonitor) SampleConfig() string {
	return `
  ## The Azure Resource ID for which metrics will be gathered
  ##   ex: resource_id = "/subscriptions/<subscription_id>/resourceGroups/<resource_group>/providers/Microsoft.Storage/storageAccounts/<storage_account>"
  # resource_id = ""
	`
}

func (a *AzureMonitor) Init() error {
	if a.ResourceId == "" {
		return errors.New("resource_id must be configured")
	}

	authorizer, err := auth.NewAuthorizerFromEnvironment()
	if err != nil {
		return err
	}
	a.authorizer = authorizer
	return nil
}

func (a *AzureMonitor) makeRequest() (*http.Response, error) {
	client := http.Client{}

	url := fmt.Sprintf("https://management.azure.com/%v/providers/microsoft.insights/metrics?api-version=2018-01-01", a.ResourceId)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req, err = autorest.CreatePreparer(a.authorizer.WithAuthorization()).Prepare(req)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func (a *AzureMonitor) Gather(acc telegraf.Accumulator) error {
	resp, err := a.makeRequest()
	if err != nil {
		return err
	}

	monitorResponse, err := parseResponse(resp)
	if err != nil {
		return err
	}

	fieldsByTimestamp := make(map[string]map[string]interface{})

	tags := make(map[string]string)
	tags["resource_id"] = a.ResourceId

	// There are two arrays in the metrics response, and nested within can be data points
	// with a variety of different RFC3339 time stamps. Here we'll bucket them all by
	// timestamp and then invoke telegraf.Accumulator.AddFields once for each different
	// timestamp.
	for _, value := range monitorResponse.Value {
		name := value.Name.Value

		for _, ts := range value.TimeSeries {
			for _, datum := range ts.Data {
				_, exists := fieldsByTimestamp[datum.TimeStamp]
				if !exists {
					fieldsByTimestamp[datum.TimeStamp] = make(map[string]interface{})
				}
				slot := fieldsByTimestamp[datum.TimeStamp]
				slot[name] = datum.Average
			}
		}
	}

	for ts, fields := range fieldsByTimestamp {
		timestamp, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			continue
		}

		acc.AddFields("azure_monitor", fields, tags, timestamp)
	}

	return nil
}

func init() {
	inputs.Add("azure_monitor", func() telegraf.Input {
		return &AzureMonitor{}
	})
}
