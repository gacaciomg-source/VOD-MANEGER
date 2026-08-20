// Package metrics concentra os coletores Prometheus do processo.
package metrics

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Registry agrupa os coletores. Não usamos o registro global do Prometheus para que
// dois processos no mesmo teste (ou dois papéis no futuro) não colidam.
type Registry struct {
	reg *prometheus.Registry

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	buildInfo    *prometheus.GaugeVec
}

// New cria o registro com os coletores da Fase 1.
func New(nodeID, role, version string) *Registry {
	reg := prometheus.NewRegistry()

	m := &Registry{
		reg: reg,
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "vodm",
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "Requisições HTTP atendidas, por método, rota e status.",
		}, []string{"method", "route", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "vodm",
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "Latência das requisições HTTP.",
			// Buckets curtos: a API administrativa deve responder em milissegundos.
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		}, []string{"method", "route"}),
		buildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "vodm",
			Name:      "build_info",
			Help:      "Identificação do processo: sempre 1, com os rótulos como informação.",
		}, []string{"node_id", "role", "version"}),
	}

	reg.MustRegister(
		m.httpRequests,
		m.httpDuration,
		m.buildInfo,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	m.buildInfo.WithLabelValues(nodeID, role, version).Set(1)
	return m
}

// Gatherer expõe o registro para o handler HTTP.
func (m *Registry) Gatherer() prometheus.Gatherer { return m.reg }

// ObserveHTTP registra uma requisição atendida.
func (m *Registry) ObserveHTTP(method, route string, status int, d time.Duration) {
	m.httpRequests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.httpDuration.WithLabelValues(method, route).Observe(d.Seconds())
}
