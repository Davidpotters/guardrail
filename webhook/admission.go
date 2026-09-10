package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// handleValidate is the one HTTP endpoint the ValidatingWebhookConfiguration
// points at. The API server calls this synchronously on every matching
// request (pod CREATE) and blocks until it gets a response -- there's no
// queue, no retry-later; whatever this returns *is* the admission decision.
func handleValidate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var review admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &review); err != nil {
		http.Error(w, fmt.Sprintf("failed to decode AdmissionReview: %v", err), http.StatusBadRequest)
		return
	}

	if review.Request == nil {
		http.Error(w, "AdmissionReview had no request", http.StatusBadRequest)
		return
	}

	var pod corev1.Pod
	if err := json.Unmarshal(review.Request.Object.Raw, &pod); err != nil {
		// Fail closed: if the object can't even be decoded, deny it rather
		// than let something malformed slip through unreviewed.
		respond(w, review.Request.UID, false, fmt.Sprintf("failed to decode Pod: %v", err))
		return
	}

	if violations := checkPolicy(&pod); len(violations) > 0 {
		respond(w, review.Request.UID, false, joinViolations(violations))
		return
	}

	respond(w, review.Request.UID, true, "")
}

func respond(w http.ResponseWriter, uid types.UID, allowed bool, reason string) {
	response := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "admission.k8s.io/v1",
			Kind:       "AdmissionReview",
		},
		Response: &admissionv1.AdmissionResponse{
			UID:     uid,
			Allowed: allowed,
		},
	}
	if !allowed {
		response.Response.Result = &metav1.Status{Message: reason}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("failed to write admission response: %v", err)
	}
}

func joinViolations(violations []string) string {
	msg := "guardrail policy violation:"
	for _, v := range violations {
		msg += "\n  - " + v
	}
	return msg
}
