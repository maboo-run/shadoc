package alerting

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/agentprotocol"
	"github.com/maboo-run/shadoc/internal/domain"
	runcontrol "github.com/maboo-run/shadoc/internal/run"
	"github.com/maboo-run/shadoc/internal/store"
)

const AgentHeartbeatTimeout = 2 * time.Minute

type ReconciliationStorage interface {
	Storage
	ListTasks(context.Context) ([]domain.Task, error)
	TaskRunHealth(context.Context) (map[string]store.TaskRunHealth, error)
	ListRepositories(context.Context) ([]domain.Repository, error)
	ListRepositoryCapacityPolicies(context.Context) ([]domain.RepositoryCapacityPolicy, error)
	RepositoryCapacityForecast(context.Context, string) (domain.RepositoryCapacityForecast, error)
	ListAgents(context.Context) ([]store.AgentRecord, error)
	ListPlans(context.Context) ([]domain.Plan, error)
	ListMaintenancePolicies(context.Context) ([]domain.MaintenancePolicy, error)
	ListRestoreVerificationPolicies(context.Context) ([]domain.RestoreVerificationPolicy, error)
	LatestRestoreVerifications(context.Context) (map[string]store.RestoreVerificationRecord, error)
	LatestSuccessfulRestoreVerifications(context.Context) (map[string]store.RestoreVerificationRecord, error)
	RestoreVerificationCleanupRequired(context.Context) (map[string]store.RestoreVerificationRecord, error)
	LatestScheduleOccurrences(context.Context, string) (map[string]store.ScheduleOccurrence, error)
}

type reconciliation struct {
	service *Service
	active  map[string]store.AlertState
	seen    map[string]bool
	errors  []error
}

func (s *Service) Reconcile(ctx context.Context) error {
	storage, ok := s.store.(ReconciliationStorage)
	if !ok {
		return errors.New("alert storage does not support health reconciliation")
	}
	tasks, err := storage.ListTasks(ctx)
	if err != nil {
		return err
	}
	runHealth, err := storage.TaskRunHealth(ctx)
	if err != nil {
		return err
	}
	repositories, err := storage.ListRepositories(ctx)
	if err != nil {
		return err
	}
	capacityPolicies, err := storage.ListRepositoryCapacityPolicies(ctx)
	if err != nil {
		return err
	}
	agents, err := storage.ListAgents(ctx)
	if err != nil {
		return err
	}
	plans, err := storage.ListPlans(ctx)
	if err != nil {
		return err
	}
	maintenance, err := storage.ListMaintenancePolicies(ctx)
	if err != nil {
		return err
	}
	restoreVerificationPolicies, err := storage.ListRestoreVerificationPolicies(ctx)
	if err != nil {
		return err
	}
	latestRestoreVerifications, err := storage.LatestRestoreVerifications(ctx)
	if err != nil {
		return err
	}
	latestSuccessfulRestoreVerifications, err := storage.LatestSuccessfulRestoreVerifications(ctx)
	if err != nil {
		return err
	}
	restoreVerificationCleanup, err := storage.RestoreVerificationCleanupRequired(ctx)
	if err != nil {
		return err
	}
	planOccurrences, err := storage.LatestScheduleOccurrences(ctx, "plan")
	if err != nil {
		return err
	}
	maintenanceOccurrences, err := storage.LatestScheduleOccurrences(ctx, "maintenance")
	if err != nil {
		return err
	}
	restoreVerificationOccurrences, err := storage.LatestScheduleOccurrences(ctx, "restore_verification")
	if err != nil {
		return err
	}
	active, err := s.Active(ctx)
	if err != nil {
		return err
	}
	current := make(map[string]store.AlertState, len(active))
	for _, state := range active {
		current[state.StateKey] = state
	}
	r := &reconciliation{service: s, active: current, seen: map[string]bool{}}
	now := s.now().UTC()

	taskNames := make(map[string]string, len(tasks))
	for _, task := range tasks {
		taskNames[task.ID] = task.Name
		r.reconcileTask(ctx, task, runHealth[task.ID], now)
	}
	repositoryNames := make(map[string]string, len(repositories))
	capacityPolicyByRepository := make(map[string]domain.RepositoryCapacityPolicy, len(capacityPolicies))
	for _, policy := range capacityPolicies {
		capacityPolicyByRepository[policy.RepositoryID] = policy
	}
	for _, repository := range repositories {
		repositoryNames[repository.ID] = repository.Name
		policy := capacityPolicyByRepository[repository.ID]
		forecast := domain.RepositoryCapacityForecast{Status: domain.CapacityForecastInsufficientSamples}
		if policy.Enabled && policy.ExhaustionWarningDays > 0 {
			forecast, err = storage.RepositoryCapacityForecast(ctx, repository.ID)
			if err != nil {
				return err
			}
		}
		r.reconcileRepository(ctx, repository, policy, forecast, now)
	}
	for _, agent := range agents {
		r.reconcileAgent(ctx, agent, now)
	}
	for _, plan := range plans {
		r.reconcilePlan(ctx, plan, planOccurrences[plan.ID])
	}
	for _, policy := range maintenance {
		name := repositoryNames[policy.RepositoryID]
		if name == "" {
			name = policy.RepositoryID
		}
		r.reconcileMaintenance(ctx, policy, name, maintenanceOccurrences[policy.RepositoryID])
	}
	for _, policy := range restoreVerificationPolicies {
		name := taskNames[policy.TaskID]
		if name == "" {
			name = policy.TaskID
		}
		r.reconcileRestoreVerification(ctx, policy, name, restoreVerificationOccurrences[policy.TaskID], latestRestoreVerifications[policy.TaskID], latestSuccessfulRestoreVerifications[policy.TaskID], restoreVerificationCleanup[policy.TaskID], now)
	}
	r.resolveRemovedObjects(ctx)
	return errors.Join(r.errors...)
}

func (s *Service) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	_ = s.Reconcile(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.Reconcile(ctx)
		}
	}
}

func (r *reconciliation) reconcileTask(ctx context.Context, task domain.Task, health store.TaskRunHealth, now time.Time) {
	runKey, staleKey := "task:"+task.ID+":run", "task:"+task.ID+":stale"
	r.seen[runKey], r.seen[staleKey] = true, true
	if !task.Enabled {
		r.resolve(ctx, runKey)
		r.resolve(ctx, staleKey)
		return
	}
	if health.Latest == nil {
		r.resolve(ctx, runKey)
	} else if status, ok := runcontrol.ParseTerminalStatus(health.Latest.Status); ok {
		if status == runcontrol.Succeeded {
			r.resolve(ctx, runKey)
		} else {
			r.raise(ctx, taskRunSignal(task, status))
		}
	}
	maxAge := task.Health.MaxSuccessAgeHours
	if maxAge == 0 {
		r.resolve(ctx, staleKey)
		return
	}
	baseline := task.CreatedAt
	if health.LastSuccessAt != nil {
		baseline = *health.LastSuccessAt
	}
	if baseline.IsZero() || now.Before(baseline.Add(time.Duration(maxAge)*time.Hour)) {
		r.resolve(ctx, staleKey)
		return
	}
	name := task.Name
	if name == "" {
		name = task.ID
	}
	r.raise(ctx, store.AlertSignal{StateKey: staleKey, Kind: "task_stale", Severity: store.AlertCritical, ObjectType: "task", ObjectID: task.ID, ObjectName: name, Reason: "长期无完整成功", Message: fmt.Sprintf("任务已超过 %d 小时没有完整成功", maxAge), TargetPage: "运行记录", RecoveryCondition: "任务完成一次完整成功运行"})
}

func (r *reconciliation) reconcileRepository(ctx context.Context, repository domain.Repository, policy domain.RepositoryCapacityPolicy, forecast domain.RepositoryCapacityForecast, now time.Time) {
	integrityKey := "repository:" + repository.ID + ":integrity"
	capacityPrefix := "repository:" + repository.ID + ":capacity-"
	lowKey, forecastKey, staleKey, probeKey := capacityPrefix+"low", capacityPrefix+"forecast", capacityPrefix+"stale", capacityPrefix+"probe"
	r.seen[integrityKey], r.seen[lowKey], r.seen[forecastKey], r.seen[staleKey], r.seen[probeKey] = true, true, true, true, true
	name := repository.Name
	if name == "" {
		name = repository.ID
	}
	if repository.Status == "abnormal" || strings.HasPrefix(repository.Status, "unprotected-partial:") {
		r.raise(ctx, store.AlertSignal{StateKey: integrityKey, Kind: "repository_abnormal", Severity: store.AlertCritical, ObjectType: "repository", ObjectID: repository.ID, ObjectName: name, Reason: "仓库状态异常", Message: "仓库完整性或部分快照保护状态异常", TargetPage: "备份仓库", RecoveryCondition: "仓库检查通过且状态恢复为 ready"})
	} else if repository.Status == "ready" || repository.Status == "uninitialized" {
		r.resolve(ctx, integrityKey)
	}
	if !policy.Enabled || repository.Status == "uninitialized" || repository.Status == "disconnected" {
		r.resolve(ctx, lowKey)
		r.resolve(ctx, forecastKey)
		r.resolve(ctx, staleKey)
		r.resolve(ctx, probeKey)
		return
	}
	if repository.Capacity == nil || repository.Capacity.TotalBytes == 0 {
		r.resolve(ctx, lowKey)
		r.resolve(ctx, forecastKey)
	} else {
		percent := float64(repository.Capacity.AvailableBytes) * 100 / float64(repository.Capacity.TotalBytes)
		belowBytes := policy.MinimumAvailableBytes > 0 && repository.Capacity.AvailableBytes < policy.MinimumAvailableBytes
		belowPercent := policy.MinimumAvailablePercent > 0 && percent < policy.MinimumAvailablePercent
		if belowBytes || belowPercent {
			r.raise(ctx, store.AlertSignal{StateKey: lowKey, Kind: "repository_capacity_low", Severity: store.AlertCritical, ObjectType: "repository", ObjectID: repository.ID, ObjectName: name, Reason: "仓库可用容量不足", Message: fmt.Sprintf("仓库可用容量为 %d 字节（%.1f%%），低于已配置阈值", repository.Capacity.AvailableBytes, percent), TargetPage: "备份仓库", RecoveryCondition: "仓库可用容量同时恢复到已配置的绝对值和百分比阈值以上"})
		} else {
			r.resolve(ctx, lowKey)
		}
		if policy.ExhaustionWarningDays > 0 && forecast.Status == domain.CapacityForecastReady && forecast.EstimatedExhaustionAt != nil && !forecast.EstimatedExhaustionAt.After(now.Add(time.Duration(policy.ExhaustionWarningDays)*24*time.Hour)) {
			r.raise(ctx, store.AlertSignal{StateKey: forecastKey, Kind: "repository_capacity_forecast", Severity: store.AlertWarning, ObjectType: "repository", ObjectID: repository.ID, ObjectName: name, Reason: "仓库容量预计即将耗尽", Message: fmt.Sprintf("按近期增长趋势，仓库容量预计在 %s 耗尽", forecast.EstimatedExhaustionAt.UTC().Format(time.RFC3339)), TargetPage: "备份仓库", RecoveryCondition: "预计耗尽时间超出配置的预警天数，或近期数据不再呈正增长"})
		} else {
			r.resolve(ctx, forecastKey)
		}
	}
	baseline := policy.UpdatedAt
	if policy.LastSuccessAt != nil {
		baseline = *policy.LastSuccessAt
	}
	if baseline.IsZero() || now.Before(baseline.Add(2*time.Duration(policy.ProbeIntervalMinutes)*time.Minute)) {
		r.resolve(ctx, staleKey)
	} else {
		r.raise(ctx, store.AlertSignal{StateKey: staleKey, Kind: "repository_capacity_stale", Severity: store.AlertWarning, ObjectType: "repository", ObjectID: repository.ID, ObjectName: name, Reason: "仓库容量数据已过期", Message: fmt.Sprintf("容量探测已超过 %d 分钟没有成功", 2*policy.ProbeIntervalMinutes), TargetPage: "备份仓库", RecoveryCondition: "后台或手动容量探测成功"})
	}
	if policy.LastError == "" {
		r.resolve(ctx, probeKey)
	} else {
		r.raise(ctx, store.AlertSignal{StateKey: probeKey, Kind: "repository_capacity_probe_failed", Severity: store.AlertWarning, ObjectType: "repository", ObjectID: repository.ID, ObjectName: name, Reason: "仓库容量探测失败", Message: policy.LastError, TargetPage: "备份仓库", RecoveryCondition: "下一次后台或手动容量探测成功"})
	}
}

func (r *reconciliation) reconcileAgent(ctx context.Context, agent store.AgentRecord, now time.Time) {
	offlineKey := "agent:" + agent.ID + ":offline"
	protocolKey := "agent:" + agent.ID + ":protocol"
	certificateKey := "agent:" + agent.ID + ":certificate-expiry"
	renewalKey := "agent:" + agent.ID + ":certificate-renewal"
	r.seen[offlineKey], r.seen[protocolKey], r.seen[certificateKey], r.seen[renewalKey] = true, true, true, true
	if agent.RevokedAt != nil || agent.UninstalledAt != nil {
		r.resolve(ctx, offlineKey)
		r.resolve(ctx, protocolKey)
		r.resolve(ctx, certificateKey)
		r.resolve(ctx, renewalKey)
		return
	}
	fresh := agent.Status == "online" && agent.LastHeartbeatAt != nil && !agent.LastHeartbeatAt.Before(now.Add(-AgentHeartbeatTimeout))
	if fresh {
		r.resolve(ctx, offlineKey)
	} else {
		message := "Agent 当前离线或尚未建立心跳"
		if agent.LastHeartbeatAt != nil {
			message = "Agent 最后心跳时间为 " + agent.LastHeartbeatAt.UTC().Format(time.RFC3339)
		}
		r.raise(ctx, store.AlertSignal{StateKey: offlineKey, Kind: "agent_offline", Severity: store.AlertCritical, ObjectType: "agent", ObjectID: agent.ID, ObjectName: agent.ID, Reason: "Agent 离线", Message: message, TargetPage: "Agent 节点", RecoveryCondition: "Agent 恢复有效心跳"})
	}

	compatible := agent.ProtocolMin >= 1 && agent.ProtocolMin <= agentprotocol.Version && agent.ProtocolMax >= agentprotocol.Version
	if compatible {
		r.resolve(ctx, protocolKey)
	} else {
		message := fmt.Sprintf("Agent 上报协议范围 %d-%d，控制服务需要协议 %d", agent.ProtocolMin, agent.ProtocolMax, agentprotocol.Version)
		if agent.ProtocolMin == 0 && agent.ProtocolMax == 0 {
			message = "Agent 尚未上报结构化协议范围；请升级后再启用新任务"
		}
		r.raise(ctx, store.AlertSignal{StateKey: protocolKey, Kind: "agent_protocol", Severity: store.AlertWarning, ObjectType: "agent", ObjectID: agent.ID, ObjectName: agent.ID, Reason: "Agent 协议不兼容", Message: message, TargetPage: "Agent 节点", RecoveryCondition: "Agent 升级并上报包含当前控制服务协议的兼容范围"})
	}

	if agent.CertificateNotAfter == nil || agent.CertificateNotAfter.After(now.Add(30*24*time.Hour)) {
		r.resolve(ctx, certificateKey)
	} else {
		remaining := agent.CertificateNotAfter.Sub(now)
		severity := store.AlertInfo
		if remaining <= 14*24*time.Hour {
			severity = store.AlertWarning
		}
		if remaining <= 7*24*time.Hour {
			severity = store.AlertCritical
		}
		r.raise(ctx, store.AlertSignal{StateKey: certificateKey, Kind: "agent_certificate_expiry", Severity: severity, ObjectType: "agent", ObjectID: agent.ID, ObjectName: agent.ID, Reason: "Agent 证书即将到期", Message: "Agent 客户端证书将在 " + agent.CertificateNotAfter.UTC().Format(time.RFC3339) + " 到期", TargetPage: "Agent 节点", RecoveryCondition: "Agent 完成滚动续期并使用新证书恢复心跳"})
	}
	if agent.RenewalStatus == "failed" {
		r.raise(ctx, store.AlertSignal{StateKey: renewalKey, Kind: "agent_certificate_renewal", Severity: store.AlertWarning, ObjectType: "agent", ObjectID: agent.ID, ObjectName: agent.ID, Reason: "Agent 证书续期失败", Message: "Agent 最近一次滚动证书续期未完成；旧证书仍保持有效", TargetPage: "Agent 节点", RecoveryCondition: "Agent 使用旧证书重试并以新证书完成有效心跳"})
	} else {
		r.resolve(ctx, renewalKey)
	}
}

func (r *reconciliation) reconcilePlan(ctx context.Context, plan domain.Plan, occurrence store.ScheduleOccurrence) {
	key := "plan:" + plan.ID + ":schedule"
	r.seen[key] = true
	if !plan.Enabled {
		r.resolve(ctx, key)
		return
	}
	r.reconcileOccurrence(ctx, key, "plan_schedule", "plan", plan.ID, plan.Name, "备份计划", occurrence)
}

func (r *reconciliation) reconcileMaintenance(ctx context.Context, policy domain.MaintenancePolicy, repositoryName string, occurrence store.ScheduleOccurrence) {
	key := "maintenance:" + policy.RepositoryID + ":schedule"
	r.seen[key] = true
	if !policy.Enabled {
		r.resolve(ctx, key)
		return
	}
	r.reconcileOccurrence(ctx, key, "maintenance_schedule", "repository", policy.RepositoryID, repositoryName, "备份仓库", occurrence)
}

func (r *reconciliation) reconcileRestoreVerification(ctx context.Context, policy domain.RestoreVerificationPolicy, taskName string, occurrence store.ScheduleOccurrence, latest, latestSuccess, cleanup store.RestoreVerificationRecord, now time.Time) {
	prefix := "restore-verification:" + policy.TaskID + ":"
	scheduleKey, resultKey, staleKey, cleanupKey := prefix+"schedule", prefix+"result", prefix+"stale", prefix+"cleanup"
	r.seen[scheduleKey], r.seen[resultKey], r.seen[staleKey], r.seen[cleanupKey] = true, true, true, true
	if !policy.Enabled {
		r.resolve(ctx, scheduleKey)
		r.resolve(ctx, staleKey)
	} else {
		r.reconcileRestoreVerificationOccurrence(ctx, scheduleKey, policy.TaskID, taskName, occurrence)
		baseline := policy.UpdatedAt
		if latestSuccess.ID != "" {
			baseline = latestSuccess.StartedAt
			if latestSuccess.FinishedAt != nil {
				baseline = *latestSuccess.FinishedAt
			}
		}
		if baseline.IsZero() || now.Before(baseline.Add(time.Duration(policy.MaximumSuccessAgeHours)*time.Hour)) {
			r.resolve(ctx, staleKey)
		} else {
			r.raise(ctx, store.AlertSignal{StateKey: staleKey, Kind: "restore_verification_stale", Severity: store.AlertCritical, ObjectType: "task", ObjectID: policy.TaskID, ObjectName: taskName, Reason: "恢复验证过期", Message: fmt.Sprintf("任务已超过 %d 小时没有成功完成真实恢复验证", policy.MaximumSuccessAgeHours), TargetPage: "快照与恢复", RecoveryCondition: "恢复验证成功并完成临时文件清理"})
		}
	}

	if latest.ID == "" {
		r.resolve(ctx, resultKey)
	} else if latest.Status == "success" {
		r.resolve(ctx, resultKey)
	} else if latest.Status != "running" {
		severity, reason := store.AlertCritical, "恢复验证失败"
		if latest.Status == "cancelled" {
			severity, reason = store.AlertInfo, "恢复验证已取消"
		} else if latest.Status == "interrupted" {
			reason = "恢复验证因服务重启中断"
		} else if latest.Status == "cleanup_required" {
			reason = "恢复验证清理失败"
		}
		message := "最近一次真实恢复验证状态为 " + latest.Status
		if latest.ErrorSummary != "" {
			message += "：" + latest.ErrorSummary
		}
		r.raise(ctx, store.AlertSignal{StateKey: resultKey, Kind: "restore_verification_result", Severity: severity, ObjectType: "task", ObjectID: policy.TaskID, ObjectName: taskName, Reason: reason, Message: message, TargetPage: "快照与恢复", RecoveryCondition: "恢复验证成功并完成临时文件清理"})
	}

	if cleanup.ID == "" {
		r.resolve(ctx, cleanupKey)
	} else {
		r.raise(ctx, store.AlertSignal{StateKey: cleanupKey, Kind: "restore_verification_cleanup", Severity: store.AlertCritical, ObjectType: "task", ObjectID: policy.TaskID, ObjectName: taskName, Reason: "恢复验证残留待清理", Message: "恢复验证临时内容尚未安全删除", TargetPage: "快照与恢复", RecoveryCondition: "重试并完成应用专属临时目录清理"})
	}
}

func (r *reconciliation) reconcileRestoreVerificationOccurrence(ctx context.Context, key, taskID, taskName string, occurrence store.ScheduleOccurrence) {
	if occurrence.ID == "" || occurrence.Status == "pending" || occurrence.Status == "running" {
		return
	}
	if occurrence.Status == "success" {
		r.resolve(ctx, key)
		return
	}
	severity, reason := store.AlertCritical, "恢复验证计划失败"
	if occurrence.Status == "cancelled" {
		severity, reason = store.AlertInfo, "恢复验证计划已取消"
	} else if occurrence.Status == "missed" {
		reason = "恢复验证计划错过"
	} else if occurrence.Status == "interrupted" {
		reason = "恢复验证计划因服务重启中断"
	}
	r.raise(ctx, store.AlertSignal{StateKey: key, Kind: "restore_verification_schedule", Severity: severity, ObjectType: "task", ObjectID: taskID, ObjectName: taskName, Reason: reason, Message: "最近一次恢复验证计划状态为 " + occurrence.Status, TargetPage: "快照与恢复", RecoveryCondition: "下一次恢复验证计划完整成功"})
}

func (r *reconciliation) reconcileOccurrence(ctx context.Context, key, kind, objectType, objectID, objectName, targetPage string, occurrence store.ScheduleOccurrence) {
	if occurrence.ID == "" || occurrence.Status == "pending" || occurrence.Status == "running" {
		return
	}
	if occurrence.Status == "success" {
		r.resolve(ctx, key)
		return
	}
	severity, reason := store.AlertCritical, "计划执行失败"
	switch occurrence.Status {
	case "partial":
		severity, reason = store.AlertWarning, "计划部分成功"
	case "cancelled":
		severity, reason = store.AlertInfo, "计划已取消"
	case "skipped":
		severity, reason = store.AlertWarning, "计划被跳过"
	case "missed":
		severity, reason = store.AlertCritical, "计划错过"
	case "interrupted":
		severity, reason = store.AlertCritical, "计划因服务重启中断"
	}
	r.raise(ctx, store.AlertSignal{StateKey: key, Kind: kind, Severity: severity, ObjectType: objectType, ObjectID: objectID, ObjectName: objectName, Reason: reason, Message: fmt.Sprintf("最近一次计划发生状态为 %s", occurrence.Status), TargetPage: targetPage, RecoveryCondition: "下一次计划发生完整成功"})
}

func (r *reconciliation) raise(ctx context.Context, signal store.AlertSignal) {
	r.seen[signal.StateKey] = true
	if current, ok := r.active[signal.StateKey]; ok && sameSignal(current.AlertSignal, signal) {
		return
	}
	state, _, err := r.service.Raise(ctx, signal)
	if err != nil {
		r.errors = append(r.errors, err)
		return
	}
	r.active[signal.StateKey] = state
}

func (r *reconciliation) resolve(ctx context.Context, key string) {
	r.seen[key] = true
	if _, ok := r.active[key]; !ok {
		return
	}
	_, _, err := r.service.Resolve(ctx, key)
	if err != nil {
		r.errors = append(r.errors, err)
		return
	}
	delete(r.active, key)
}

func (r *reconciliation) resolveRemovedObjects(ctx context.Context) {
	managed := map[string]bool{"task_run": true, "task_stale": true, "repository_abnormal": true, "repository_capacity": true, "repository_capacity_low": true, "repository_capacity_forecast": true, "repository_capacity_stale": true, "repository_capacity_probe_failed": true, "agent_offline": true, "agent_protocol": true, "agent_certificate_expiry": true, "agent_certificate_renewal": true, "plan_schedule": true, "maintenance_schedule": true, "restore_verification_schedule": true, "restore_verification_result": true, "restore_verification_stale": true, "restore_verification_cleanup": true}
	for key, state := range r.active {
		if managed[state.Kind] && !r.seen[key] {
			r.resolve(ctx, key)
		}
	}
}

func sameSignal(left, right store.AlertSignal) bool {
	return left.StateKey == right.StateKey && left.Kind == right.Kind && left.Severity == right.Severity && left.ObjectType == right.ObjectType && left.ObjectID == right.ObjectID && left.ObjectName == right.ObjectName && left.Reason == right.Reason && left.Message == right.Message && left.TargetPage == right.TargetPage && left.RecoveryCondition == right.RecoveryCondition
}
