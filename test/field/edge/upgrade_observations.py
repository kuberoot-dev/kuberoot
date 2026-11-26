"""Shared E2 final service gate, based solely on external request evidence."""
import json


def evaluate(path):
    rows = [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
    metrics = {'driver_overloads': sum(r.get('error') == 'driver_overload' for r in rows)}
    errors = []
    if metrics['driver_overloads']:
        errors.append('observer overloaded; offered load was not maintained')
    end = max((r['scheduled_seconds'] for r in rows), default=0)
    metrics['final_service_window_end_seconds'] = end
    metrics['final_service_window_seconds'] = 60
    for kind, rate in [('http', 20), ('append', 10), ('api', .2)]:
        group = [r for r in rows if r['kind'] == kind and end - 60 < r['scheduled_seconds'] <= end]
        failures = sum(not r.get('ok') for r in group)
        metrics['final_60s_' + kind + '_requests'] = len(group)
        metrics['final_60s_' + kind + '_failures'] = failures
        if end < 60 or len(group) < int(60 * rate * .99):
            errors.append(kind + ': insufficient final 60-second offered-load evidence')
        if failures:
            errors.append(kind + ': service not healthy throughout final 60 seconds')
    metrics['wrong_http_responses'] = sum(r['kind'] == 'http' and r.get('error') == 'unexpected response' for r in rows)
    return metrics, errors
