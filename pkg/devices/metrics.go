package devices

import (
	"net/http"
	"sort"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	upDesc = prometheus.NewDesc("kuberoot_device_up",
		"1 when the last reads of the device succeeded.", []string{"device", "protocol"}, nil)
	valueDesc = prometheus.NewDesc("kuberoot_device_value",
		"A value of the device, as last read.", []string{"device", "protocol", "point", "unit"}, nil)
	lastReadDesc = prometheus.NewDesc("kuberoot_device_last_read_timestamp_seconds",
		"When the device was last read successfully.", []string{"device", "protocol"}, nil)
	beatHealthyDesc = prometheus.NewDesc("kuberoot_heartbeat_healthy",
		"1 when the heartbeat's last beat went.", []string{"heartbeat"}, nil)
	beatSentDesc = prometheus.NewDesc("kuberoot_heartbeat_sent_total",
		"Beats sent since kuberoot-devices started.", []string{"heartbeat"}, nil)
)

// Describe and Collect make the controller a Prometheus collector: every
// device's values, read at scrape time from what the node last read.
func (c *Controller) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{upDesc, valueDesc, lastReadDesc, beatHealthyDesc, beatSentDesc} {
		ch <- d
	}
}

func (c *Controller) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for name, d := range c.devices {
		d.mu.Lock()
		proto := d.spec.Protocol
		ch <- prometheus.MustNewConstMetric(upDesc, prometheus.GaugeValue, boolValue(d.connected), name, proto)
		if !d.lastOK.IsZero() {
			ch <- prometheus.MustNewConstMetric(lastReadDesc, prometheus.GaugeValue, float64(d.lastOK.UnixNano())/1e9, name, proto)
		}
		units := map[string]string{}
		for _, p := range d.spec.Points {
			units[p.Name] = p.Unit
		}
		keys := make([]string, 0, len(d.reading))
		for k := range d.reading {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ch <- prometheus.MustNewConstMetric(valueDesc, prometheus.GaugeValue, d.reading[k], name, proto, k, units[k])
		}
		d.mu.Unlock()
	}
	for name, h := range c.beats {
		h.mu.Lock()
		ch <- prometheus.MustNewConstMetric(beatHealthyDesc, prometheus.GaugeValue, boolValue(h.healthy), name)
		ch <- prometheus.MustNewConstMetric(beatSentDesc, prometheus.CounterValue, float64(h.sent), name)
		h.mu.Unlock()
	}
}

// MetricsHandler serves the controller's metrics.
func (c *Controller) MetricsHandler() http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}
