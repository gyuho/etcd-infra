// Package httputil provides HTTP helpers, retry logic, and download utilities.
//
// WHY NAME: "http": HTTP client utilities with retry and download support.
// WHY PATH: Under pkg/ because artifact downloads, health checks, and API calls across the project need HTTP helpers.
// OWNS: Retryable HTTP client, file download with progress, and response parsing helpers.
// DOES NOT OWN: service health checks or artifact caching.
package httputil
