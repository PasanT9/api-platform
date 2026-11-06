package models

import "testing"

func TestBudgetHandleReferences(t *testing.T) {
	base := BudgetDocument{
		APIVersion: BudgetAPIVersion, Kind: "Budget", Metadata: BudgetMetadata{Name: "team-monthly"},
		Spec: BudgetSpec{DisplayName: "Team monthly", Limit: BudgetLimit{Amount: "10.00", Period: "month", Action: "block"},
			Resource: &BudgetResource{Kind: "LlmProvider", ID: "anthropic"},
			Consumers: []BudgetConsumer{{Type: "apiKey", ID: "support-prod-key", Owner: &BudgetKeyOwner{Kind: "LlmProxy", ID: "support-proxy"}}}},
	}
	if err := NormalizeAndValidateBudget(&base); err != nil {
		t.Fatalf("valid handle references: %v", err)
	}
	missingOwner := base
	missingOwner.Spec.Consumers = []BudgetConsumer{{Type: "apiKey", ID: "support-prod-key"}}
	if err := NormalizeAndValidateBudget(&missingOwner); err == nil {
		t.Fatal("API key name without owner was accepted")
	}
	wrongOwner := base
	wrongOwner.Spec.Consumers = []BudgetConsumer{{Type: "apiKey", ID: "support-prod-key", Owner: &BudgetKeyOwner{Kind: "RestApi", ID: "support"}}}
	if err := NormalizeAndValidateBudget(&wrongOwner); err == nil {
		t.Fatal("unsupported API key owner was accepted")
	}
}
