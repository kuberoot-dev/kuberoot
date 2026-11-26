"""E2 kuberoot attempt budget using the already measured cold-boot allowance."""
import json


def apply(record, records_path):
    if record.get('scenario_id') != 'E2' or not record.get('product', '').startswith('kuberoot-'):
        return
    if record.get('fault_phase') in ['tampered', 'untrusted', 'healthy-control']:
        return
    product = 'kuberoot-v0.1.0' if record['product'] == 'kuberoot-v0.1.0-testkey' else 'kuberoot-main-1b29543'
    rows = [json.loads(line) for line in records_path.read_text().splitlines()]
    baseline = [r for r in rows if r.get('scenario_id') == 'E-B2' and r.get('product') == product and r.get('measurement_protocol') == 'boot-readiness-v2' and r.get('status') in ['passed', 'measured']]
    assert {r.get('repetition') for r in baseline} >= set(range(1, 11)), 'Cold-boot allowance requires all ten valid trials'
    allowance = max(r['metrics']['new_pod_serves_seconds'] for r in baseline)
    budget = 3 * (300 + allowance)
    metrics = record['metrics']
    metrics.update({'rollback_attempts': 3, 'probation_seconds_per_attempt': 300, 'measured_boot_allowance_seconds': allowance, 'rollback_budget_seconds': budget, 'boot_allowance_reference_product': product, 'boot_allowance_definition': 'maximum power-on to new pod serving across ten boot-readiness-v2 trials; same reference for fixed-main reruns'})
    elapsed = metrics.get('automatic_rollback_seconds')
    if elapsed is not None:
        metrics['rollback_within_attempt_budget'] = elapsed <= budget
        if elapsed > budget and record['status'] != 'invalid-fixture':
            record['status'] = 'failed'
            record['reason'] += '; automatic rollback exceeded 3 x (300 seconds + measured boot allowance)'
