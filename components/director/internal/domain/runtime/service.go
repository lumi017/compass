package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kyma-incubator/compass/components/director/internal/labelfilter"
	"github.com/kyma-incubator/compass/components/director/internal/model"

	"github.com/kyma-incubator/compass/components/director/internal/domain/tenant"
	"github.com/pkg/errors"
)

//go:generate mockery -name=RuntimeRepository -output=automock -outpkg=automock -case=underscore
type RuntimeRepository interface {
	Exists(ctx context.Context, tenant, id string) (bool, error)
	GetByID(ctx context.Context, tenant, id string) (*model.Runtime, error)
	GetByFiltersGlobal(ctx context.Context, filter []*labelfilter.LabelFilter) (*model.Runtime, error)
	List(ctx context.Context, tenant string, filter []*labelfilter.LabelFilter, pageSize int, cursor string) (*model.RuntimePage, error)
	Create(ctx context.Context, item *model.Runtime) error
	Update(ctx context.Context, item *model.Runtime) error
	Delete(ctx context.Context, tenant, id string) error
}

//go:generate mockery -name=LabelRepository -output=automock -outpkg=automock -case=underscore
type LabelRepository interface {
	GetByKey(ctx context.Context, tenant string, objectType model.LabelableObject, objectID, key string) (*model.Label, error)
	ListForObject(ctx context.Context, tenant string, objectType model.LabelableObject, objectID string) (map[string]*model.Label, error)
	Delete(ctx context.Context, tenant string, objectType model.LabelableObject, objectID string, key string) error
	DeleteAll(ctx context.Context, tenant string, objectType model.LabelableObject, objectID string) error
}

//go:generate mockery -name=LabelUpsertService -output=automock -outpkg=automock -case=underscore
type LabelUpsertService interface {
	UpsertMultipleLabels(ctx context.Context, tenant string, objectType model.LabelableObject, objectID string, labels map[string]interface{}) error
	UpsertLabel(ctx context.Context, tenant string, labelInput *model.LabelInput) error
}

//go:generate mockery -name=ScenariosService -output=automock -outpkg=automock -case=underscore
type ScenariosService interface {
	EnsureScenariosLabelDefinitionExists(ctx context.Context, tenant string) error
	AddDefaultScenarioIfEnabled(labels *map[string]interface{})
}

//go:generate mockery -name=ScenarioAssignmentEngine -output=automock -outpkg=automock -case=underscore
type ScenarioAssignmentEngine interface {
	GetScenariosForSelectorLabels(ctx context.Context, inputLabels map[string]string) ([]string, error)
	MergeScenariosFromInputLabelsAndAssignments(ctx context.Context, inputLabels map[string]interface{}) ([]interface{}, error)
	MergeScenarios(baseScenarios, scenariosToDelete, scenariosToAdd []interface{}) []interface{}
}

//go:generate mockery -name=UIDService -output=automock -outpkg=automock -case=underscore
type UIDService interface {
	Generate() string
}

type service struct {
	repo      RuntimeRepository
	labelRepo LabelRepository

	labelUpsertService       LabelUpsertService
	uidService               UIDService
	scenariosService         ScenariosService
	scenarioAssignmentEngine ScenarioAssignmentEngine
}

func NewService(repo RuntimeRepository,
	labelRepo LabelRepository,
	scenariosService ScenariosService,
	labelUpsertService LabelUpsertService,
	uidService UIDService,
	scenarioAssignmentEngine ScenarioAssignmentEngine) *service {
	return &service{
		repo:                     repo,
		labelRepo:                labelRepo,
		scenariosService:         scenariosService,
		labelUpsertService:       labelUpsertService,
		uidService:               uidService,
		scenarioAssignmentEngine: scenarioAssignmentEngine}
}

func (s *service) List(ctx context.Context, filter []*labelfilter.LabelFilter, pageSize int, cursor string) (*model.RuntimePage, error) {
	rtmTenant, err := tenant.LoadFromContext(ctx)
	if err != nil {
		return nil, errors.Wrapf(err, "while loading tenant from context")
	}

	if pageSize < 1 || pageSize > 100 {
		return nil, errors.New("page size must be between 1 and 100")
	}

	return s.repo.List(ctx, rtmTenant, filter, pageSize, cursor)
}

func (s *service) Get(ctx context.Context, id string) (*model.Runtime, error) {
	rtmTenant, err := tenant.LoadFromContext(ctx)
	if err != nil {
		return nil, errors.Wrapf(err, "while loading tenant from context")
	}

	runtime, err := s.repo.GetByID(ctx, rtmTenant, id)
	if err != nil {
		return nil, errors.Wrapf(err, "while getting Runtime with ID %s", id)
	}

	return runtime, nil
}

func (s *service) GetByTokenIssuer(ctx context.Context, issuer string) (*model.Runtime, error) {
	const (
		consoleURLLabelKey = "runtime_consoleUrl"
		dexSubdomain       = "dex"
		consoleSubdomain   = "console"
	)
	consoleURL := strings.Replace(issuer, dexSubdomain, consoleSubdomain, 1)

	filters := []*labelfilter.LabelFilter{
		labelfilter.NewForKeyWithQuery(consoleURLLabelKey, fmt.Sprintf(`"%s"`, consoleURL)),
	}

	runtime, err := s.repo.GetByFiltersGlobal(ctx, filters)
	if err != nil {
		return nil, errors.Wrapf(err, "while getting the Runtime by the console URL label (%s)", consoleURL)
	}

	return runtime, nil
}

func (s *service) Exist(ctx context.Context, id string) (bool, error) {
	rtmTenant, err := tenant.LoadFromContext(ctx)
	if err != nil {
		return false, errors.Wrapf(err, "while loading tenant from context")
	}

	exist, err := s.repo.Exists(ctx, rtmTenant, id)
	if err != nil {
		return false, errors.Wrapf(err, "while getting Runtime with ID %s", id)
	}

	return exist, nil
}

func (s *service) Create(ctx context.Context, in model.RuntimeInput) (string, error) {
	rtmTenant, err := tenant.LoadFromContext(ctx)
	if err != nil {
		return "", errors.Wrapf(err, "while loading tenant from context")
	}
	id := s.uidService.Generate()
	rtm := in.ToRuntime(id, rtmTenant, time.Now(), time.Now())

	err = s.repo.Create(ctx, rtm)
	if err != nil {
		return "", errors.Wrapf(err, "while creating Runtime")
	}

	err = s.scenariosService.EnsureScenariosLabelDefinitionExists(ctx, rtmTenant)
	if err != nil {
		return "", errors.Wrapf(err, "while ensuring Label Definition with key %s exists", model.ScenariosKey)
	}

	scenarios, err := s.scenarioAssignmentEngine.MergeScenariosFromInputLabelsAndAssignments(ctx, in.Labels)
	if err != nil {
		return "", errors.Wrap(err, "while merging scenarios from input and assignments")
	}

	if len(scenarios) > 0 {
		in.Labels[model.ScenariosKey] = scenarios
	} else {
		s.scenariosService.AddDefaultScenarioIfEnabled(&in.Labels)
	}

	err = s.labelUpsertService.UpsertMultipleLabels(ctx, rtmTenant, model.RuntimeLabelableObject, id, in.Labels)
	if err != nil {
		return id, errors.Wrapf(err, "while creating multiple labels for Runtime")
	}

	return id, nil
}

func (s *service) Update(ctx context.Context, id string, in model.RuntimeInput) error {
	rtmTenant, err := tenant.LoadFromContext(ctx)
	if err != nil {
		return errors.Wrapf(err, "while loading tenant from context")
	}

	rtm, err := s.Get(ctx, id)
	if err != nil {
		return errors.Wrap(err, "while getting Runtime")
	}

	rtm = in.ToRuntime(id, rtm.Tenant, rtm.CreationTimestamp, time.Now())

	err = s.repo.Update(ctx, rtm)
	if err != nil {
		return errors.Wrap(err, "while updating Runtime")
	}

	err = s.labelRepo.DeleteAll(ctx, rtmTenant, model.RuntimeLabelableObject, id)
	if err != nil {
		return errors.Wrapf(err, "while deleting all labels for Runtime")
	}

	if in.Labels == nil {
		return nil
	}

	scenarios, err := s.scenarioAssignmentEngine.MergeScenariosFromInputLabelsAndAssignments(ctx, in.Labels)
	if err != nil {
		return errors.Wrap(err, "while merging scenarios from input and assignments")
	}

	if len(scenarios) > 0 {
		in.Labels[model.ScenariosKey] = scenarios
	}

	err = s.labelUpsertService.UpsertMultipleLabels(ctx, rtmTenant, model.RuntimeLabelableObject, id, in.Labels)
	if err != nil {
		return errors.Wrapf(err, "while creating multiple labels for Runtime")
	}

	return nil
}

func (s *service) Delete(ctx context.Context, id string) error {
	rtmTenant, err := tenant.LoadFromContext(ctx)
	if err != nil {
		return errors.Wrapf(err, "while loading tenant from context")
	}

	err = s.repo.Delete(ctx, rtmTenant, id)
	if err != nil {
		return errors.Wrapf(err, "while deleting Runtime")
	}

	// All labels are deleted (cascade delete)

	return nil
}

func (s *service) SetLabel(ctx context.Context, labelInput *model.LabelInput) error {
	rtmTenant, err := tenant.LoadFromContext(ctx)
	if err != nil {
		return errors.Wrapf(err, "while loading tenant from context")
	}

	err = s.ensureRuntimeExists(ctx, rtmTenant, labelInput.ObjectID)
	if err != nil {
		return err
	}

	currentRuntimeLabels, err := s.getCurrentLabelsForRuntime(ctx, rtmTenant, labelInput.ObjectID)
	if err != nil {
		return err
	}

	newRuntimeLabels := make(map[string]interface{})
	for k, v := range currentRuntimeLabels {
		newRuntimeLabels[k] = v
	}

	newRuntimeLabels[labelInput.Key] = labelInput.Value

	err = s.upsertScenariosLabelIfShould(ctx, labelInput.ObjectID, labelInput.Key, currentRuntimeLabels, newRuntimeLabels)
	if err != nil {
		return err
	}

	if labelInput.Key != model.ScenariosKey {
		err = s.labelUpsertService.UpsertLabel(ctx, rtmTenant, labelInput)
		if err != nil {
			return errors.Wrapf(err, "while creating label for Runtime")
		}
	}

	return nil
}

func (s *service) GetLabel(ctx context.Context, runtimeID string, key string) (*model.Label, error) {
	rtmTenant, err := tenant.LoadFromContext(ctx)
	if err != nil {
		return nil, errors.Wrapf(err, "while loading tenant from context")
	}

	rtmExists, err := s.repo.Exists(ctx, rtmTenant, runtimeID)
	if err != nil {
		return nil, errors.Wrap(err, "while checking Runtime existence")
	}
	if !rtmExists {
		return nil, fmt.Errorf("Runtime with ID %s doesn't exist", runtimeID)
	}

	label, err := s.labelRepo.GetByKey(ctx, rtmTenant, model.RuntimeLabelableObject, runtimeID, key)
	if err != nil {
		return nil, errors.Wrap(err, "while getting label for Runtime")
	}

	return label, nil
}

func (s *service) ListLabels(ctx context.Context, runtimeID string) (map[string]*model.Label, error) {
	rtmTenant, err := tenant.LoadFromContext(ctx)
	if err != nil {
		return nil, errors.Wrapf(err, "while loading tenant from context")
	}

	rtmExists, err := s.repo.Exists(ctx, rtmTenant, runtimeID)
	if err != nil {
		return nil, errors.Wrap(err, "while checking Runtime existence")
	}

	if !rtmExists {
		return nil, fmt.Errorf("Runtime with ID %s doesn't exist", runtimeID)
	}

	labels, err := s.labelRepo.ListForObject(ctx, rtmTenant, model.RuntimeLabelableObject, runtimeID)
	if err != nil {
		return nil, errors.Wrap(err, "while getting label for Runtime")
	}

	return labels, nil
}

func (s *service) DeleteLabel(ctx context.Context, runtimeID string, key string) error {
	rtmTenant, err := tenant.LoadFromContext(ctx)
	if err != nil {
		return errors.Wrapf(err, "while loading tenant from context")
	}

	err = s.ensureRuntimeExists(ctx, rtmTenant, runtimeID)
	if err != nil {
		return err
	}

	currentRuntimeLabels, err := s.getCurrentLabelsForRuntime(ctx, rtmTenant, runtimeID)
	if err != nil {
		return err
	}

	newRuntimeLabels := make(map[string]interface{})
	for k, v := range currentRuntimeLabels {
		newRuntimeLabels[k] = v
	}

	delete(newRuntimeLabels, key)

	err = s.upsertScenariosLabelIfShould(ctx, runtimeID, key, currentRuntimeLabels, newRuntimeLabels)
	if err != nil {
		return err
	}

	if key != model.ScenariosKey {
		err = s.labelRepo.Delete(ctx, rtmTenant, model.RuntimeLabelableObject, runtimeID, key)
		if err != nil {
			return errors.Wrapf(err, "while deleting Runtime label")
		}
	}

	return nil
}

func (s *service) ensureRuntimeExists(ctx context.Context, tnt string, runtimeID string) error {
	rtmExists, err := s.repo.Exists(ctx, tnt, runtimeID)
	if err != nil {
		return errors.Wrap(err, "while checking Runtime existence")
	}
	if !rtmExists {
		return fmt.Errorf("Runtime with ID %s doesn't exist", runtimeID)
	}

	return nil
}

func (s *service) upsertScenariosLabelIfShould(ctx context.Context, runtimeID string, modifiedLabelKey string, currentRuntimeLabels, newRuntimeLabels map[string]interface{}) error {
	rtmTenant, err := tenant.LoadFromContext(ctx)
	if err != nil {
		return errors.Wrapf(err, "while loading tenant from context")
	}

	finalScenarios := make([]interface{}, 0)

	if modifiedLabelKey == model.ScenariosKey {
		scenarios, err := s.scenarioAssignmentEngine.MergeScenariosFromInputLabelsAndAssignments(ctx, newRuntimeLabels)
		if err != nil {
			return errors.Wrap(err, "while merging scenarios from input and assignments")
		}

		for _, scenario := range scenarios {
			finalScenarios = append(finalScenarios, scenario)
		}
	} else {
		oldScenariosLabel, err := getScenariosLabel(currentRuntimeLabels)
		if err != nil {
			return err
		}

		previousScenariosFromAssignments, err := s.getScenariosFromAssignments(ctx, currentRuntimeLabels)
		if err != nil {
			return errors.Wrap(err, "while getting old scenarios label and scenarios from assignments")
		}

		newScenariosFromAssignments, err := s.getScenariosFromAssignments(ctx, newRuntimeLabels)
		if err != nil {
			return errors.Wrap(err, "while getting new scenarios from assignments")
		}

		finalScenarios = s.scenarioAssignmentEngine.MergeScenarios(oldScenariosLabel, previousScenariosFromAssignments, newScenariosFromAssignments)
	}

	//TODO compare finalScenarios and oldScenariosLabel to determine when to delete scenarios label
	if len(finalScenarios) == 0 {
		err := s.labelRepo.Delete(ctx, rtmTenant, model.RuntimeLabelableObject, runtimeID, model.ScenariosKey)
		if err != nil {
			return errors.Wrapf(err, "while deleting scenarios label from runtime with id [%s]", runtimeID)
		}
		return nil
	}

	scenariosLabelInput := &model.LabelInput{
		Key:        model.ScenariosKey,
		Value:      finalScenarios,
		ObjectID:   runtimeID,
		ObjectType: model.RuntimeLabelableObject,
	}

	err = s.labelUpsertService.UpsertLabel(ctx, rtmTenant, scenariosLabelInput)
	if err != nil {
		return errors.Wrapf(err, "while creating scenarios label for Runtime with id [%s]", runtimeID)
	}

	return nil
}

func (s *service) getCurrentLabelsForRuntime(ctx context.Context, tenantID, runtimeID string) (map[string]interface{}, error) {
	labels, err := s.labelRepo.ListForObject(ctx, tenantID, model.RuntimeLabelableObject, runtimeID)
	if err != nil {
		return nil, err
	}

	currentLabels := make(map[string]interface{})
	for _, v := range labels {
		currentLabels[v.Key] = v.Value
	}
	return currentLabels, nil
}

func getScenariosLabel(currentRuntimeLabels map[string]interface{}) ([]interface{}, error) {
	oldScenariosLabel, ok := currentRuntimeLabels[model.ScenariosKey]

	var oldScenariosLabelInterfaceSlice []interface{}
	if ok {
		oldScenariosLabelInterfaceSlice, ok = oldScenariosLabel.([]interface{})
		if !ok {
			return nil, errors.New("value for scenarios label must be []interface{}")
		}
	}
	return oldScenariosLabelInterfaceSlice, nil
}

func (s *service) getScenariosFromAssignments(ctx context.Context, currentRuntimeLabels map[string]interface{}) ([]interface{}, error) {
	selectors := s.convertMapStringInterfaceToMapStringString(currentRuntimeLabels)

	ScenariosFromAssignments, err := s.scenarioAssignmentEngine.GetScenariosForSelectorLabels(ctx, selectors)
	if err != nil {
		return nil, errors.Wrap(err, "while getting scenarios for selector labels")
	}

	newScenariosInterfaceSlice := s.convertStringSliceToInterfaceSlice(ScenariosFromAssignments)

	return newScenariosInterfaceSlice, nil
}

func (s *service) convertMapStringInterfaceToMapStringString(in map[string]interface{}) map[string]string {
	out := make(map[string]string)

	for k, v := range in {
		val, ok := v.(string)
		if ok {
			out[k] = val
		}
	}

	return out
}

func (s *service) convertStringSliceToInterfaceSlice(in []string) []interface{} {
	out := make([]interface{}, 0)
	for _, v := range in {
		out = append(out, v)
	}

	return out
}
