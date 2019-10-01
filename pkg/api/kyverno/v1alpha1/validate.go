package v1alpha1

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"github.com/golang/glog"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (p ClusterPolicy) Validate() error {
	var errs []error

	for _, rule := range p.Spec.Rules {
		if ruleErrs := rule.Validate(); ruleErrs != nil {
			errs = append(errs, ruleErrs...)
		}
	}

	if err := p.ValidateUniqueRuleName(); err != nil {
		errs = append(errs, err)
	}

	return joinErrs(errs)
}

// ValidateUniqueRuleName checks if the rule names are unique across a policy
func (p ClusterPolicy) ValidateUniqueRuleName() error {
	var ruleNames []string

	for _, rule := range p.Spec.Rules {
		if containString(ruleNames, rule.Name) {
			return fmt.Errorf(`duplicate rule name: '%s'`, rule.Name)
		}
		ruleNames = append(ruleNames, rule.Name)
	}
	return nil
}

// Validate checks if rule is not empty and all substructures are valid
func (r Rule) Validate() []error {
	var errs []error

	// only one type of rule is allowed per rule
	if err := r.ValidateRuleType(); err != nil {
		errs = append(errs, err)
	}

	// validate resource description block
	if err := r.MatchResources.ResourceDescription.Validate(); err != nil {
		errs = append(errs, err)
	}

	if err := r.ExcludeResources.ResourceDescription.Validate(); err != nil {
		errs = append(errs, err)
	}

	// validate validation rule
	if err := r.ValidateOverlayPattern(); err != nil {
		errs = append(errs, err)
	}

	if patternErrs := r.ValidateExistingAnchor(); patternErrs != nil {
		errs = append(errs, patternErrs...)
	}

	return errs
}

// validateOverlayPattern checks one of pattern/anyPattern must exist
func (r Rule) ValidateOverlayPattern() error {
	if reflect.DeepEqual(r.Validation, Validation{}) {
		return nil
	}

	if r.Validation.Pattern == nil && len(r.Validation.AnyPattern) == 0 {
		return fmt.Errorf("neither pattern nor anyPattern found in rule '%s'", r.Name)
	}

	if r.Validation.Pattern != nil && len(r.Validation.AnyPattern) != 0 {
		return fmt.Errorf("either pattern or anyPattern is allowed in rule '%s'", r.Name)
	}

	return nil
}

// validateRuleType checks only one type of rule is defined per rule
func (r Rule) ValidateRuleType() error {
	mutate := r.HasMutate()
	validate := r.HasValidate()
	generate := r.HasGenerate()

	if !mutate && !validate && !generate {
		return fmt.Errorf("no rule defined in '%s'", r.Name)
	}

	if (mutate && !validate && !generate) ||
		(!mutate && validate && !generate) ||
		(!mutate && !validate && generate) {
		return nil
	}

	return fmt.Errorf("multiple types of rule defined in rule '%s', only one type of rule is allowed per rule", r.Name)
}

func (r Rule) HasMutate() bool {
	return !reflect.DeepEqual(r.Mutation, Mutation{})
}

func (r Rule) HasValidate() bool {
	return !reflect.DeepEqual(r.Validation, Validation{})
}

func (r Rule) HasGenerate() bool {
	return !reflect.DeepEqual(r.Generation, Generation{})
}

// Validate checks if all necesarry fields are present and have values. Also checks a Selector.
// field type is checked through openapi
// Returns error if
// - kinds is empty array, i.e. kinds: []
// - selector is invalid
func (rd ResourceDescription) Validate() error {
	if reflect.DeepEqual(rd, ResourceDescription{}) {
		return nil
	}

	if len(rd.Kinds) == 0 {
		return errors.New("field Kind is not specified")
	}

	if rd.Selector != nil {
		selector, err := metav1.LabelSelectorAsSelector(rd.Selector)
		if err != nil {
			return err
		}
		requirements, _ := selector.Requirements()
		if len(requirements) == 0 {
			return errors.New("the requirements are not specified in selector")
		}
	}

	return nil
}

// Validate if all mandatory PolicyPatch fields are set
func (pp *Patch) Validate() error {
	if pp.Path == "" {
		return errors.New("JSONPatch field 'path' is mandatory")
	}

	if pp.Operation == "add" || pp.Operation == "replace" {
		if pp.Value == nil {
			return fmt.Errorf("JSONPatch field 'value' is mandatory for operation '%s'", pp.Operation)
		}

		return nil
	} else if pp.Operation == "remove" {
		return nil
	}

	return fmt.Errorf("Unsupported JSONPatch operation '%s'", pp.Operation)
}

// Validate returns error if generator is configured incompletely
func (gen *Generation) Validate() error {
	if gen.Data == nil && gen.Clone == (CloneFrom{}) {
		return fmt.Errorf("Neither data nor clone (source) of %s is specified", gen.Kind)
	}
	if gen.Data != nil && gen.Clone != (CloneFrom{}) {
		return fmt.Errorf("Both data nor clone (source) of %s are specified", gen.Kind)
	}
	return nil
}

// ValidateExistingAnchor
// existing acnchor must define on array
func (r Rule) ValidateExistingAnchor() []error {
	var errs []error

	if r.Validation.Pattern != nil {
		if _, err := validateExistingAnchorOnPattern(r.Validation.Pattern, "/"); err != nil {
			errs = append(errs, err)
		}
	}

	if len(r.Validation.AnyPattern) != 0 {
		for _, pattern := range r.Validation.AnyPattern {
			if _, err := validateExistingAnchorOnPattern(pattern, "/"); err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errs
}

// validateExistingAnchorOnPattern validates ^() only defined on array
func validateExistingAnchorOnPattern(pattern interface{}, path string) (string, error) {
	switch typedPattern := pattern.(type) {
	case map[string]interface{}:
		return validateMap(typedPattern, path)
	case []interface{}:
		return validateArray(typedPattern, path)
	case string, float64, int, int64, bool, nil:
		// check on type string
		if checkedPattern := reflect.ValueOf(pattern); checkedPattern.Kind() == reflect.String {
			if hasAnchor, str := hasExistingAnchor(checkedPattern.String()); hasAnchor {
				return path, fmt.Errorf("existing anchor at %s must be of type array, found: %T", path+str, checkedPattern.Kind())
			}
		}

		// return nil on all other cases
		return "", nil
	default:
		glog.V(4).Infof("Pattern contains unknown type %T. Path: %s", pattern, path)
		return path, fmt.Errorf("pattern contains unknown type, path: %s", path)
	}
}

func validateMap(pattern map[string]interface{}, path string) (string, error) {
	for key, patternElement := range pattern {
		if hasAnchor, str := hasExistingAnchor(key); hasAnchor {
			if checkedPattern := reflect.ValueOf(patternElement); checkedPattern.Kind() != reflect.Slice {
				return path, fmt.Errorf("existing anchor at %s must be of type array, found: %T", path+str, patternElement)
			}
		}

		if path, err := validateExistingAnchorOnPattern(patternElement, path+key+"/"); err != nil {
			return path, err
		}
	}

	return "", nil
}

func validateArray(patternArray []interface{}, path string) (string, error) {
	if len(patternArray) == 0 {
		return path, fmt.Errorf("pattern array at %s is empty", path)
	}

	for i, pattern := range patternArray {
		currentPath := path + strconv.Itoa(i) + "/"
		if path, err := validateExistingAnchorOnPattern(pattern, currentPath); err != nil {
			return path, err
		}
	}

	return "", nil
}
