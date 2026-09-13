#!/bin/sh
set -eu
umask 077

usage() {
  echo "usage: $0 --scenario no-task|callback|workers|burst --minutes 5|10|20 --pid-file ABS_FILE --out ABS_JSONL" >&2
  exit 2
}

scenario=
minutes=
pid_file=
out=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --scenario) scenario=${2-}; shift 2 ;;
    --minutes) minutes=${2-}; shift 2 ;;
    --pid-file) pid_file=${2-}; shift 2 ;;
    --out) out=${2-}; shift 2 ;;
    *) usage ;;
  esac
done
case "$scenario:$minutes" in no-task:5|callback:5|workers:10|burst:20) ;; *) usage ;; esac
case "$pid_file:$out" in /*:/*) ;; *) usage ;; esac
[ -f "$pid_file" ] && [ ! -L "$pid_file" ] || { echo "pid_file_unsafe" >&2; exit 1; }
[ ! -e "$out" ] || { echo "out_exists" >&2; exit 1; }
case "$(uname -s)" in Darwin) ;; *) echo "unsupported_os" >&2; exit 1 ;; esac

end=$(( $(date +%s) + minutes * 60 ))
printf '{"schema_version":1,"scenario":"%s","minutes":%s,"started_unix":%s}\n' "$scenario" "$minutes" "$(date +%s)" > "$out"
chmod 600 "$out"

while [ "$(date +%s)" -lt "$end" ]; do
  now=$(date +%s)
  while IFS= read -r pid; do
    case "$pid" in ""|*[!0-9]*) echo "pid_file_invalid" >&2; exit 1 ;; esac
    sample=$(ps -o pid=,ppid=,rss=,%cpu= -p "$pid" 2>/dev/null || true)
    if [ -n "$sample" ]; then
      set -- $sample
      printf '{"sample_unix":%s,"pid":%s,"ppid":%s,"rss_kib":%s,"cpu_percent":%s}\n' "$now" "$1" "$2" "$3" "$4" >> "$out"
    else
      printf '{"sample_unix":%s,"pid":%s,"state":"exited"}\n' "$now" "$pid" >> "$out"
    fi
  done < "$pid_file"
  sleep 1
done

printf '{"completed_unix":%s}\n' "$(date +%s)" >> "$out"
