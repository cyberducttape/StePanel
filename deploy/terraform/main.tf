resource "kubernetes_namespace" "stepanel" {
  metadata { name = "stepanel" }
}

resource "kubernetes_service" "stepanel" {
  metadata {
    name      = "stepanel"
    namespace = kubernetes_namespace.stepanel.metadata[0].name
    labels    = { "app.kubernetes.io/name" = "stepanel" }
  }
  spec {
    selector = { "app.kubernetes.io/name" = "stepanel" }
    port {
      name        = "http"
      port        = 8080
      target_port = "http"
    }
  }
}

resource "kubernetes_deployment" "stepanel" {
  metadata {
    name      = "stepanel"
    namespace = kubernetes_namespace.stepanel.metadata[0].name
    labels    = { "app.kubernetes.io/name" = "stepanel" }
  }
  spec {
    replicas = 1
    strategy { type = "Recreate" }
    selector {
      match_labels = { "app.kubernetes.io/name" = "stepanel" }
    }
    template {
      metadata { labels = { "app.kubernetes.io/name" = "stepanel" } }
      spec {
        automount_service_account_token = false
        # Match the application's two-hour restore drain plus a five-minute margin.
        termination_grace_period_seconds = 7500
        security_context {
          run_as_non_root        = true
          run_as_user            = 10001
          run_as_group           = 10001
          fs_group               = 10001
          fs_group_change_policy = "OnRootMismatch"
          seccomp_profile { type = "RuntimeDefault" }
        }
        container {
          name              = "stepanel"
          image             = var.image
          image_pull_policy = "IfNotPresent"
          port {
            name           = "http"
            container_port = 8080
          }
          security_context {
            allow_privilege_escalation = false
            read_only_root_filesystem  = true
            capabilities { drop = ["ALL"] }
          }
          env {
            name  = "STEPANEL_ENV"
            value = "production"
          }
          env {
            name  = "STEPANEL_LISTEN"
            value = ":8080"
          }
          env {
            name  = "STEPANEL_TLS_TERMINATED"
            value = "1"
          }
          env {
            name  = "STEPANEL_REQUIRE_OFFSITE_BACKUP"
            value = "1"
          }
          env {
            name = "STEPANEL_OFFSITE_TARGET"
            value_from {
              secret_key_ref {
                name = "stepanel-secrets"
                key  = "offsite-target"
              }
            }
          }
          env {
            name = "STEPANEL_ENVIRONMENT_KEY"
            value_from {
              secret_key_ref {
                name = "stepanel-secrets"
                key  = "environment-key"
              }
            }
          }
          env {
            name = "STEPANEL_BACKUP_SIGNING_KEY"
            value_from {
              secret_key_ref {
                name = "stepanel-secrets"
                key  = "backup-signing-key"
              }
            }
          }
          env {
            name = "STEPANEL_BACKUP_ENCRYPTION_KEY"
            value_from {
              secret_key_ref {
                name = "stepanel-secrets"
                key  = "backup-encryption-key"
              }
            }
          }
          env {
            name  = "STEPANEL_IMPORT_ROOT"
            value = "/var/lib/ste-panel/imports"
          }
          env {
            name  = "STEPANEL_BACKUP_ROOT"
            value = "/var/lib/ste-panel/backups"
          }
          env {
            name  = "STEPANEL_WEB_ROOT"
            value = "/var/www"
          }
          env {
            name  = "STEPANEL_MAIL_ROOT"
            value = "/var/lib/ste-panel/mail"
          }
          env {
            name  = "STEPANEL_NVM_DIR"
            value = "/var/lib/ste-panel/nvm"
          }
          env {
            name  = "STEPANEL_PROXY_ROOT"
            value = "/var/lib/ste-panel/proxy"
          }
          env {
            name  = "STEPANEL_VHOST_ROOT"
            value = "/var/lib/ste-panel/vhosts"
          }
          env {
            name  = "STEPANEL_APP_ROOT"
            value = "/var/lib/ste-panel/apps"
          }
          env {
            name  = "STEPANEL_MALWARE_ROOT"
            value = "/var/lib/ste-panel/quarantine"
          }
          env {
            name  = "STEPANEL_AUDIT_LOG"
            value = "/var/lib/ste-panel/audit.jsonl"
          }
          env {
            name  = "STEPANEL_JOB_STATE"
            value = "/var/lib/ste-panel/jobs.json"
          }
          env {
            name  = "STEPANEL_SESSION_STATE"
            value = "/var/lib/ste-panel/sessions.json"
          }
          env {
            name  = "STEPANEL_CONTROL_PLANE_DB"
            value = "/var/lib/ste-panel/control-plane.db"
          }
          env {
            name  = "STEPANEL_RECOVERY_ROOT"
            value = "/var/www/sites/.stepanel-recovery"
          }
          env {
            name = "STEPANEL_ADMIN_PASSWORD"
            value_from {
              secret_key_ref {
                name = "stepanel-secrets"
                key  = "admin-password"
              }
            }
          }
          env {
            name = "STEPANEL_SESSION_SECRET"
            value_from {
              secret_key_ref {
                name = "stepanel-secrets"
                key  = "session-secret"
              }
            }
          }
          env {
            name = "STEPANEL_AUDIT_KEY"
            value_from {
              secret_key_ref {
                name = "stepanel-secrets"
                key  = "audit-key"
              }
            }
          }
          env {
            name = "STEPANEL_ADMIN_TOTP_SECRET"
            value_from {
              secret_key_ref {
                name = "stepanel-secrets"
                key  = "admin-totp-secret"
              }
            }
          }
          env {
            name = "STEPANEL_ACCOUNT_KEY"
            value_from {
              secret_key_ref {
                name = "stepanel-secrets"
                key  = "account-key"
              }
            }
          }
          readiness_probe {
            http_get {
              path = "/readyz"
              port = "http"
            }
            period_seconds    = 10
            timeout_seconds   = 2
            failure_threshold = 3
          }
          liveness_probe {
            http_get {
              path = "/livez"
              port = "http"
            }
            initial_delay_seconds = 15
            period_seconds        = 20
            timeout_seconds       = 2
            failure_threshold     = 3
          }
          resources {
            requests = { cpu = "100m", memory = "128Mi" }
            limits   = { cpu = "500m", memory = "512Mi" }
          }
          volume_mount {
            name       = "stepanel-data"
            mount_path = "/var/lib/ste-panel"
          }
          volume_mount {
            name       = "stepanel-sites"
            mount_path = "/var/www/sites"
          }
          volume_mount {
            name       = "tmp"
            mount_path = "/tmp"
          }
        }
        volume {
          name = "stepanel-data"
          persistent_volume_claim { claim_name = kubernetes_persistent_volume_claim.stepanel_data.metadata[0].name }
        }
        volume {
          name = "stepanel-sites"
          persistent_volume_claim { claim_name = kubernetes_persistent_volume_claim.stepanel_sites.metadata[0].name }
        }
        volume {
          name = "tmp"
          empty_dir { size_limit = "64Mi" }
        }
      }
    }
  }
}

resource "kubernetes_persistent_volume_claim" "stepanel_data" {
  metadata {
    name      = "stepanel-data"
    namespace = kubernetes_namespace.stepanel.metadata[0].name
  }
  spec {
    access_modes = ["ReadWriteOnce"]
    resources { requests = { storage = "50Gi" } }
  }
}

resource "kubernetes_persistent_volume_claim" "stepanel_sites" {
  metadata {
    name      = "stepanel-sites"
    namespace = kubernetes_namespace.stepanel.metadata[0].name
  }
  spec {
    access_modes = ["ReadWriteOnce"]
    resources { requests = { storage = "50Gi" } }
  }
}

resource "kubernetes_pod_disruption_budget_v1" "stepanel" {
  metadata {
    name      = "stepanel"
    namespace = kubernetes_namespace.stepanel.metadata[0].name
  }
  spec {
    min_available = 1
    selector {
      match_labels = { "app.kubernetes.io/name" = "stepanel" }
    }
  }
}

resource "kubernetes_network_policy_v1" "stepanel_ingress" {
  metadata {
    name      = "stepanel-ingress"
    namespace = kubernetes_namespace.stepanel.metadata[0].name
  }
  spec {
    pod_selector { match_labels = { "app.kubernetes.io/name" = "stepanel" } }
    policy_types = ["Ingress"]
    ingress {
      from {
        namespace_selector { match_labels = { "stepanel.ingress" = "true" } }
      }
      ports {
        protocol = "TCP"
        port     = 8080
      }
    }
  }
}
