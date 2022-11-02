package historian

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/services/annotations"
	"github.com/grafana/grafana/pkg/services/dashboards"
	ngmodels "github.com/grafana/grafana/pkg/services/ngalert/models"
	"github.com/grafana/grafana/pkg/services/ngalert/state"
)

// AnnotationStateHistorian is an implementation of state.Historian that uses Grafana Annotations as the backing datastore.
type AnnotationStateHistorian struct {
	annotations annotations.Repository
	dashboards  *dashboardResolver
	log         log.Logger
}

func NewAnnotationHistorian(annotations annotations.Repository, dashboards dashboards.DashboardService) *AnnotationStateHistorian {
	return &AnnotationStateHistorian{
		annotations: annotations,
		dashboards:  newDashboardResolver(dashboards, defaultDashboardCacheExpiry),
		log:         log.New("ngalert.state.historian"),
	}
}

// RecordStates writes a number of state transitions for a given rule to state history.
func (h *AnnotationStateHistorian) RecordStates(ctx context.Context, rule *ngmodels.AlertRule, states []state.StateTransition) {
	logger := h.log.New(rule.GetKey().LogContext()...)
	// Build annotations before starting goroutine, to make sure all data is copied and won't mutate underneath us.
	annotations := h.buildAnnotations(rule, states, logger)
	panel := parsePanelKey(rule, logger)
	go h.recordAnnotationsSync(ctx, panel, annotations, logger)
}

func (h *AnnotationStateHistorian) buildAnnotations(rule *ngmodels.AlertRule, states []state.StateTransition, logger log.Logger) []annotations.Item {
	items := make([]annotations.Item, 0, len(states))
	for _, state := range states {
		logger.Debug("Alert state changed creating annotation", "newState", state.Formatted(), "oldState", state.PreviousFormatted())

		labels := removePrivateLabels(state.Labels)
		annotationText := fmt.Sprintf("%s {%s} - %s", rule.Title, labels.String(), state.Formatted())

		item := annotations.Item{
			AlertId:   rule.ID,
			OrgId:     state.OrgID,
			PrevState: state.PreviousFormatted(),
			NewState:  state.Formatted(),
			Text:      annotationText,
			Epoch:     state.LastEvaluationTime.UnixNano() / int64(time.Millisecond),
		}

		items = append(items, item)
	}
	return items
}

// panelKey uniquely identifies a panel.
type panelKey struct {
	orgID   int64
	dashUID string
	panelID int64
}

// panelKey attempts to get the key of the panel attached to the given rule. Returns nil if the rule is not attached to a panel.
func parsePanelKey(rule *ngmodels.AlertRule, logger log.Logger) *panelKey {
	dashUID, ok := rule.Annotations[ngmodels.DashboardUIDAnnotation]
	if ok {
		panelAnno := rule.Annotations[ngmodels.PanelIDAnnotation]
		panelID, err := strconv.ParseInt(panelAnno, 10, 64)
		if err != nil {
			logger.Error("Error parsing panelUID for alert annotation", "actual", panelAnno, "error", err)
			return nil
		}
		return &panelKey{
			orgID:   rule.OrgID,
			dashUID: dashUID,
			panelID: panelID,
		}
	}
	return nil
}

func (h *AnnotationStateHistorian) recordAnnotationsSync(ctx context.Context, panel *panelKey, annotations []annotations.Item, logger log.Logger) {
	if panel != nil {
		dashID, err := h.dashboards.getID(ctx, panel.orgID, panel.dashUID)
		if err != nil {
			logger.Error("Error getting dashboard for alert annotation", "dashboardUID", panel.dashUID, "error", err)
			return
		}

		for _, i := range annotations {
			i.DashboardId = dashID
			i.PanelId = panel.panelID
		}
	}

	if err := h.annotations.SaveMany(ctx, annotations); err != nil {
		logger.Error("Error saving alert annotation batch", "error", err)
	}
}

func removePrivateLabels(labels data.Labels) data.Labels {
	result := make(data.Labels)
	for k, v := range labels {
		if !strings.HasPrefix(k, "__") && !strings.HasSuffix(k, "__") {
			result[k] = v
		}
	}
	return result
}
