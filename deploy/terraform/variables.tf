variable "kubeconfig" {
  description = "Path to the target cluster kubeconfig."
  type        = string
  default     = "~/.kube/config"
}

variable "image" {
  description = "Immutable StePanel image reference."
  type        = string
  default     = "ghcr.io/cyberducttape/stepanel@sha256:23ac17a2092ce8153ff85dc7662883bedb018615b016586a3643f96d22cdd6d0"
}
