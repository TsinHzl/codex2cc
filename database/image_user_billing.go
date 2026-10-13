package database

import (
	"fmt"
	"math"
	"strings"
)

const (
	UserBillingModeToken    = "token"
	UserBillingModePerImage = "per_image"
)

// SupportsImageBilling deliberately excludes text models with inline image tools.
// Those requests retain their existing token billing policy.
func SupportsImageBilling(model string) bool {
	model = normalizeBillingModelName(model)
	return strings.HasPrefix(model, "gpt-image-")
}

func ValidateModelUserBilling(model string, o ModelPricingOverride) error {
	if o.UserBillingMode != "" && o.UserBillingMode != UserBillingModeToken && o.UserBillingMode != UserBillingModePerImage {
		return fmt.Errorf("user_billing_mode must be token or per_image")
	}
	if math.IsNaN(o.ImageUnitPrice) || math.IsInf(o.ImageUnitPrice, 0) || o.ImageUnitPrice < 0 {
		return fmt.Errorf("image_unit_price must be a finite non-negative USD amount")
	}
	if o.UserBillingMode == UserBillingModePerImage {
		if !SupportsImageBilling(model) {
			return fmt.Errorf("per_image billing requires an image model")
		}
		if o.ImageUnitPrice <= 0 {
			return fmt.Errorf("image_unit_price must be greater than zero for per_image billing")
		}
	}
	return nil
}

// UserBilling records the policy and successful image count at settlement time.
// Empty mode identifies legacy rows; an explicit per_image zero is never replaced
// with upstream token cost when displaying failed/undelivered outputs.
type UserBilling struct {
	UserBillingMode  string  `json:"user_billing_mode"`
	ImageUnitPrice   float64 `json:"image_unit_price"`
	BilledImageCount int     `json:"billed_image_count"`
}

type usageBillingSnapshot struct {
	UserBilling
	accountCost float64
}

// SnapshotUsageLogBilling freezes image pricing before scope counters and the
// buffered database writer observe an event. It does not mutate the caller's log.
func SnapshotUsageLogBilling(input *UsageLogInput) *UsageLogInput {
	if input == nil || input.billingSnapshot != nil {
		return input
	}
	model := input.EffectiveModel
	if model == "" {
		model = input.Model
	}
	if !SupportsImageBilling(model) {
		return input
	}
	p := GetModelPricing(model)
	mode := UserBillingModeToken
	if p.UserBillingMode == UserBillingModePerImage && p.ImageUnitPrice > 0 {
		mode = UserBillingModePerImage
	}
	snapshot := &usageBillingSnapshot{UserBilling: UserBilling{UserBillingMode: mode}, accountCost: UsageLogBilledCost(input)}
	if mode == UserBillingModePerImage {
		snapshot.ImageUnitPrice = p.ImageUnitPrice
		if input.StatusCode >= 200 && input.StatusCode < 300 && !input.IsRetryAttempt && input.ErrorMessage == "" {
			snapshot.BilledImageCount = max(0, input.ImageCount)
		}
	}
	copy := *input
	copy.billingSnapshot = snapshot
	return &copy
}

func (input *UsageLogInput) UserBillingDetails() UserBilling {
	if input != nil && input.billingSnapshot != nil {
		return input.billingSnapshot.UserBilling
	}
	return UserBilling{}
}

// WithDeliveredImageCount caps fees after Studio has persisted the outputs.
// Upstream usage/cost remain intact even if local storage fails.
func WithDeliveredImageCount(input *UsageLogInput, delivered int) *UsageLogInput {
	input = SnapshotUsageLogBilling(input)
	if input == nil || input.UserBillingDetails().UserBillingMode != UserBillingModePerImage {
		return input
	}
	copy, snapshot := *input, *input.billingSnapshot
	snapshot.BilledImageCount = min(snapshot.BilledImageCount, max(0, delivered))
	copy.billingSnapshot = &snapshot
	return &copy
}

func UsageLogUserBilledCost(input *UsageLogInput) float64 {
	input = SnapshotUsageLogBilling(input)
	if input == nil {
		return 0
	}
	if s := input.billingSnapshot; s != nil {
		if s.UserBillingMode == UserBillingModePerImage {
			return float64(s.BilledImageCount) * s.ImageUnitPrice
		}
		return s.accountCost
	}
	return UsageLogBilledCost(input)
}
