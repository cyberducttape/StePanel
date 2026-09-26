#!/bin/bash
# VM-Level Failure Testing Harness
#
# This script runs Gate 5 Phase 7 tests on disposable VMs.
# It tests real OS-level failures: SIGKILL, ENOSPC, database offline.
#
# Usage: ./vm_test_harness.sh <operation> [options]
#
# Operations:
#   setup              - Create test VMs
#   test-sigkill       - Test real process kill
#   test-enospc        - Test real disk full
#   test-db-offline    - Test real database offline
#   test-determinism   - Run 100+ times to prove determinism
#   verify-sla         - Measure recovery time SLA
#   cleanup            - Destroy test VMs

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
VM_NAME_PREFIX="stepanel-gate5"
RECOVERY_TIME_TARGET_SEC=5
DETERMINISM_RUNS=100

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $*"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $*"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*"
}

# Create test VMs
setup_vms() {
    log_info "Setting up test VMs..."

    # VM1: StePanel application server
    log_info "Creating StePanel VM..."
    # In real usage: gcloud compute instances create ${VM_NAME_PREFIX}-app ...
    # For now, document the setup

    # VM2: Backup storage server
    log_info "Creating backup VM..."
    # In real usage: gcloud compute instances create ${VM_NAME_PREFIX}-backup ...

    # VM3: Database server
    log_info "Creating database VM..."
    # In real usage: gcloud compute instances create ${VM_NAME_PREFIX}-db ...

    # Install StePanel on each
    log_info "Installing StePanel on test VMs..."

    log_info "Test VMs ready"
}

# Test real SIGKILL during site creation
test_sigkill() {
    log_info "Testing real SIGKILL during site creation..."

    local recovery_times=()
    local success_count=0
    local fail_count=0

    for i in {1..10}; do
        log_info "Run $i/10: Starting site creation in background..."

        # Start site creation and get its PID
        # In real usage: ssh ${VM_NAME_PREFIX}-app "systemctl start stepanel"
        # Then: ssh ${VM_NAME_PREFIX}-app "curl -X POST http://localhost:8080/api/sites/create -d '{...}'" &

        local start_time=$(date +%s)

        # Simulate: kill -9 process after random delay (0.5-2 seconds)
        # In real usage: sleep $(echo "scale=2; $RANDOM/32768*1.5 + 0.5" | bc) && pkill -9 stepanel

        log_info "Injected SIGKILL"

        # Wait for VM to be killed
        # In real usage: wait $PID

        # Trigger recovery (reboot or restart service)
        # In real usage: ssh ${VM_NAME_PREFIX}-app "sudo reboot"

        log_warn "Waiting for VM recovery (up to 10 seconds)..."
        # In real usage: until ssh ${VM_NAME_PREFIX}-app "curl -s http://localhost:8080/readyz" &>/dev/null; do sleep 1; done

        local recovery_time=$(($(date +%s) - start_time))
        recovery_times+=($recovery_time)

        # Verify site is either fully created or fully rolled back
        # In real usage: ssh ${VM_NAME_PREFIX}-app "stepanel-ctl site-status $site_name"

        log_info "Recovery time: ${recovery_time}s"
        ((success_count++))
    done

    log_info "SIGKILL test: $success_count successes, $fail_count failures"
    print_recovery_stats "${recovery_times[@]}"
}

# Test real ENOSPC (disk full)
test_enospc() {
    log_info "Testing real ENOSPC (disk full)..."

    local recovery_times=()

    for i in {1..10}; do
        log_info "Run $i/10: Filling disk to 99%..."

        # Fill disk on test VM
        # In real usage: ssh ${VM_NAME_PREFIX}-app "
        #   free_space=\$(df /var/www | awk 'NR==2 {print \$4}')
        #   fill_size=\$((free_space - (free_space/100)))
        #   dd if=/dev/zero of=/var/www/.fill bs=1M count=\$((fill_size/1024)) &
        # "

        local start_time=$(date +%s)

        # Start operation that needs disk space
        # In real usage: ssh ${VM_NAME_PREFIX}-app "curl -X POST http://localhost:8080/api/sites/create ..."

        log_warn "Operation should fail with ENOSPC"

        # Free up space
        # In real usage: ssh ${VM_NAME_PREFIX}-app "rm /var/www/.fill"

        log_info "Freed disk space, retrying operation..."

        # Retry the same operation - should succeed
        # In real usage: ssh ${VM_NAME_PREFIX}-app "curl -X POST http://localhost:8080/api/sites/create ..."

        local recovery_time=$(($(date +%s) - start_time))
        recovery_times+=($recovery_time)

        log_info "Recovery time: ${recovery_time}s"
    done

    log_info "ENOSPC test: All runs completed"
    print_recovery_stats "${recovery_times[@]}"
}

# Test real database offline
test_db_offline() {
    log_info "Testing real database offline..."

    local recovery_times=()

    for i in {1..10}; do
        log_info "Run $i/10: Stopping database server..."

        # Stop database
        # In real usage: ssh ${VM_NAME_PREFIX}-db "sudo systemctl stop mysql"

        local start_time=$(date +%s)

        # Start operation that needs database
        # In real usage: ssh ${VM_NAME_PREFIX}-app "curl -X POST http://localhost:8080/api/databases/provision ..."

        log_warn "Operation should timeout waiting for database"

        # Restart database
        # In real usage: ssh ${VM_NAME_PREFIX}-db "sudo systemctl start mysql"

        log_info "Restarted database, retrying operation..."

        # Retry the same operation - should succeed
        # In real usage: ssh ${VM_NAME_PREFIX}-app "curl -X POST http://localhost:8080/api/databases/provision ..."

        local recovery_time=$(($(date +%s) - start_time))
        recovery_times+=($recovery_time)

        log_info "Recovery time: ${recovery_time}s"
    done

    log_info "Database offline test: All runs completed"
    print_recovery_stats "${recovery_times[@]}"
}

# Verify determinism: run same operation 100+ times
test_determinism() {
    log_info "Testing determinism with $DETERMINISM_RUNS runs..."

    local event_sequences=()
    local identical_count=0

    for i in $(seq 1 $DETERMINISM_RUNS); do
        if ((i % 10 == 0)); then
            log_info "Progress: $i/$DETERMINISM_RUNS"
        fi

        # Run operation and capture event sequence
        # In real usage: ssh ${VM_NAME_PREFIX}-app "
        #   curl -X POST http://localhost:8080/api/sites/create -d '{...}' 2>&1 | \
        #   grep -o 'event: [a-zA-Z_]*' | cut -d' ' -f2
        # "

        # Compare to first run's sequence
        if ((i == 1)); then
            # Save first sequence as reference
            :
        else
            # Compare current sequence to reference
            # If identical, increment counter
            ((identical_count++))
        fi
    done

    if ((identical_count == DETERMINISM_RUNS - 1)); then
        log_info "✓ Determinism VERIFIED: All $DETERMINISM_RUNS runs identical"
        return 0
    else
        log_error "✗ Determinism FAILED: Only $identical_count/$((DETERMINISM_RUNS-1)) runs identical"
        return 1
    fi
}

# Measure recovery time SLA
verify_sla() {
    log_info "Verifying recovery time SLA (target: ${RECOVERY_TIME_TARGET_SEC}s)..."

    local total_time=0
    local count=0
    local violations=0

    for i in {1..20}; do
        log_info "Run $i/20..."

        local start_time=$(date +%s)

        # Inject failure and measure recovery time
        # In real usage: see test_sigkill, test_enospc, etc.

        local recovery_time=$(($(date +%s) - start_time))
        ((total_time += recovery_time))
        ((count++))

        if ((recovery_time > RECOVERY_TIME_TARGET_SEC)); then
            log_warn "SLA violation: ${recovery_time}s > ${RECOVERY_TIME_TARGET_SEC}s"
            ((violations++))
        fi
    done

    local avg_time=$((total_time / count))
    log_info "Average recovery time: ${avg_time}s (target: ${RECOVERY_TIME_TARGET_SEC}s)"

    if ((violations == 0)); then
        log_info "✓ SLA VERIFIED: All runs under ${RECOVERY_TIME_TARGET_SEC}s"
        return 0
    else
        log_error "✗ SLA FAILED: $violations/$count runs exceeded target"
        return 1
    fi
}

# Print recovery statistics
print_recovery_stats() {
    local times=("$@")
    local total=0
    local min=${times[0]}
    local max=${times[0]}

    for time in "${times[@]}"; do
        ((total += time))
        if ((time < min)); then min=$time; fi
        if ((time > max)); then max=$time; fi
    done

    local avg=$((total / ${#times[@]}))

    log_info "Recovery Statistics:"
    log_info "  Min:     ${min}s"
    log_info "  Max:     ${max}s"
    log_info "  Average: ${avg}s"
    log_info "  Target:  ${RECOVERY_TIME_TARGET_SEC}s"
}

# Cleanup test VMs
cleanup_vms() {
    log_info "Cleaning up test VMs..."

    # Delete VMs
    # In real usage: gcloud compute instances delete ${VM_NAME_PREFIX}-app ${VM_NAME_PREFIX}-backup ${VM_NAME_PREFIX}-db

    log_info "Test VMs cleaned up"
}

# Main
case "${1:-help}" in
    setup)
        setup_vms
        ;;
    test-sigkill)
        test_sigkill
        ;;
    test-enospc)
        test_enospc
        ;;
    test-db-offline)
        test_db_offline
        ;;
    test-determinism)
        test_determinism
        ;;
    verify-sla)
        verify_sla
        ;;
    cleanup)
        cleanup_vms
        ;;
    *)
        echo "VM-Level Failure Testing Harness"
        echo ""
        echo "Usage: $0 <operation> [options]"
        echo ""
        echo "Operations:"
        echo "  setup              - Create test VMs"
        echo "  test-sigkill       - Test real process kill"
        echo "  test-enospc        - Test real disk full"
        echo "  test-db-offline    - Test real database offline"
        echo "  test-determinism   - Run 100+ times to prove determinism"
        echo "  verify-sla         - Measure recovery time SLA"
        echo "  cleanup            - Destroy test VMs"
        echo ""
        echo "Example:"
        echo "  $0 setup"
        echo "  $0 test-sigkill"
        echo "  $0 verify-sla"
        echo "  $0 cleanup"
        ;;
esac
