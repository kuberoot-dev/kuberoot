"""Shared E2 final service gate, based solely on external request evidence."""
import datetime,json,shlex,time


def evaluate(path):
    rows = [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
    return evaluate_rows(rows)


def evaluate_rows(rows):
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


def tail_window(path):
    # Bound observer-side diagnostic work; two MiB covers far more than 60 s.
    with path.open('rb') as source:
        source.seek(0, 2)
        offset = max(0, source.tell() - 2 * 1024**2)
        source.seek(offset)
        if offset:
            source.readline()
        rows = []
        for line in source:
            try:
                rows.append(json.loads(line))
            except json.JSONDecodeError:
                pass  # a concurrent final write will be read on the next poll
    metrics, errors = evaluate_rows(rows)
    latest = max((datetime.datetime.fromisoformat(r['utc']).timestamp() for r in rows if r.get('utc')), default=0)
    if time.time() - latest > 10:
        errors.append('observer evidence is stale')
    return {'metrics': metrics, 'errors': errors}


def wait_for_service(gateway, name, maximum=240):
    """Allow recovery, then require 60 s of real uninterrupted W/API success."""
    start = time.monotonic()
    time.sleep(65)
    latest = {'errors': ['not sampled']}
    while True:
        result = gateway('python3 /home/observer/upgrade_observations.py ' + shlex.quote('/home/observer/' + name + '.jsonl'))
        if result.returncode:
            latest = {'errors': [result.stderr]}
        else:
            latest = json.loads(result.stdout)
        waited = time.monotonic() - start
        print(json.dumps({'utc': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'post_terminal_observation_seconds': waited, 'service_window_errors': latest['errors']}), flush=True)
        if not latest['errors'] or waited >= maximum:
            break
        time.sleep(min(5, maximum - waited))
    metrics = {'post_terminal_observation_seconds': waited, 'post_terminal_observation_limit_seconds': maximum, 'sustained_service_window_observed': not bool(latest['errors'])}
    errors = [] if not latest['errors'] else ['no fresh 60-second sustained W/API window within 240 seconds after terminal observation']
    return metrics, errors


if __name__ == '__main__':
    import pathlib, sys
    print(json.dumps(tail_window(pathlib.Path(sys.argv[1]))))
