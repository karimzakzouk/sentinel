package lab

import (
	"context"
	"crypto/sha1"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/karimzakzouk/sentinel/internal/capture"
	"github.com/karimzakzouk/sentinel/internal/model"
)

const (
	seedMax       = 20
	seedKeyPrefix = "sentinel:lab:seed:"
	seedTable     = "sentinel_lab_seed"
	seedTopic     = "sentinel-lab-seed"
	seedQueue     = "sentinel.lab.seed"
)

// seedItem is one body to write into an empty datastore, with a stable key
// derived from the capture path when known.
type seedItem struct {
	Key, Body string
}

// SeedFidelity says what captured bodies will do to empty datastores before write replay.
func SeedFidelity(plan *Plan, samples []capture.BodySample) []string {
	if len(samples) == 0 || len(plan.Empty) == 0 {
		return nil
	}
	n := min(len(samples), seedMax)
	var out []string
	seen := map[string]bool{}
	for _, ref := range plan.Empty {
		_, _, _, engine, _, ok := emptyDatastore(plan, ref)
		if !ok {
			continue
		}
		family := seedFamily(engine)
		if family == "" {
			out = append(out, fmt.Sprintf("%s runs an empty %s: seeding from captured bodies is not run yet", ref, engine))
			continue
		}
		if seen[family] {
			continue
		}
		seen[family] = true
		out = append(out, seedFidelityNote(family, n))
	}
	return out
}

func seedFidelityNote(family string, n int) string {
	switch family {
	case "redis":
		return fmt.Sprintf("before write replay, empty redis or valkey datastores are seeded with %d captured bodies under %s…",
			n, seedKeyPrefix)
	case "postgres":
		return fmt.Sprintf("before write replay, empty postgres datastores are seeded with %d captured bodies as rows in %s", n, seedTable)
	case "mysql":
		return fmt.Sprintf("before write replay, empty mysql or mariadb datastores are seeded with %d captured bodies as rows in %s", n, seedTable)
	case "mongo":
		return fmt.Sprintf("before write replay, empty mongo datastores are seeded with %d captured bodies as documents in sentinel.%s", n, seedTable)
	case "kafka":
		return fmt.Sprintf("before write replay, empty kafka datastores are seeded with %d captured bodies as records on topic %s", n, seedTopic)
	case "rabbitmq":
		return fmt.Sprintf("before write replay, empty rabbitmq datastores are seeded with %d captured bodies as messages on queue %s", n, seedQueue)
	default:
		return fmt.Sprintf("before write replay, empty %s datastores are seeded with %d captured bodies", family, n)
	}
}

// SeedEmptyDatastores runs one-shot seed pods against empty datastore copies
// the Lab knows how to mark non-empty (redis, valkey, postgres, mysql, mongo, kafka, rabbitmq).
func (p *Prover) SeedEmptyDatastores(ctx context.Context, ns string, built *Built, plan *Plan, samples []capture.BodySample) ([]string, error) {
	notes := SeedFidelity(plan, samples)
	if len(samples) == 0 {
		return notes, nil
	}
	bodies := seedBodies(samples)
	if len(bodies) == 0 {
		return notes, nil
	}
	var created []string
	defer func() {
		if len(created) == 0 {
			return
		}
		core := p.Client.CoreV1()
		ctx := context.WithoutCancel(ctx)
		for _, name := range created {
			_ = core.Pods(ns).Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: ptr.To(int64(0))})
		}
	}()
	sealImage := ""
	if p != nil {
		sealImage = p.SealImage
	}
	for _, ref := range plan.Empty {
		image, host, port, engine, env, ok := emptyDatastore(plan, ref)
		if !ok || seedFamily(engine) == "" {
			continue
		}
		ip := serviceIP(built, ref.Name)
		if ip == "" {
			return notes, fmt.Errorf("no Service for empty datastore %s", ref.Name)
		}
		name := "lab-seed-" + ref.Name
		pod := seedPod(ns, name, image, host, port, engine, bodies, env, sealImage)
		allow := append(built.Allow, podAllow(ip, seedPorts(engine, port)...)...)
		pod = p.seal(pod, allow)
		if _, err := p.Client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			return notes, fmt.Errorf("start seed for %s: %w", ref, err)
		}
		created = append(created, name)
	}
	for _, name := range created {
		if err := p.waitPodSucceeded(ctx, ns, name, time.Minute); err != nil {
			return notes, err
		}
	}
	return notes, nil
}

func seedBodies(samples []capture.BodySample) []seedItem {
	var out []seedItem
	for _, s := range samples {
		if len(out) >= seedMax {
			break
		}
		body := s.Body
		if body == "" {
			body = s.ResponseBody
		}
		if body == "" {
			continue
		}
		key := seedKeyPrefix + fmt.Sprint(len(out))
		if path := strings.TrimSpace(s.Path); path != "" {
			sum := sha1.Sum([]byte(s.Method + " " + path))
			key = seedKeyPrefix + fmt.Sprintf("%x", sum[:6])
		}
		out = append(out, seedItem{Key: key, Body: body})
	}
	return out
}

func emptyDatastore(plan *Plan, ref model.ObjectRef) (image, host string, port int32, engine string, env []corev1.EnvVar, ok bool) {
	key := ref.Kind + "." + ref.Name
	for _, pod := range plan.Pods {
		if pod.Labels[CopyOf] != key {
			continue
		}
		eng, found := datastoreEngine(pod.Spec)
		if !found || len(pod.Spec.Containers) == 0 {
			return "", "", 0, "", nil, false
		}
		port = defaultSeedPort(eng)
		for _, cp := range pod.Spec.Containers[0].Ports {
			if cp.ContainerPort > 0 {
				port = cp.ContainerPort
				break
			}
		}
		return pod.Spec.Containers[0].Image, ref.Name, port, eng, credentialEnv(pod.Spec.Containers[0].Env), true
	}
	return "", "", 0, "", nil, false
}

// credentialEnv keeps literal credential-shaped variables from an empty
// datastore copy (honeytokens already substituted) for the seed pod.
func credentialEnv(env []corev1.EnvVar) []corev1.EnvVar {
	var out []corev1.EnvVar
	for _, e := range env {
		if e.ValueFrom != nil || e.Value == "" || !isCredentialEnv(e.Name) {
			continue
		}
		out = append(out, corev1.EnvVar{Name: e.Name, Value: e.Value})
	}
	return out
}

func isCredentialEnv(name string) bool {
	u := strings.ToUpper(name)
	for _, p := range []string{"PG", "MYSQL", "MONGO", "RABBITMQ", "KAFKA", "POSTGRES", "MARIADB"} {
		if strings.HasPrefix(u, p) {
			return true
		}
	}
	switch u {
	case "PASSWORD", "PASS", "USER", "USERNAME", "DATABASE", "DB",
		"DB_USER", "DB_USERNAME", "DB_PASSWORD", "DB_PASS", "DB_NAME", "DB_DATABASE",
		"ROOT_PASSWORD", "ROOT_USER":
		return true
	}
	for _, suf := range []string{"_PASSWORD", "_PASS", "_USER", "_USERNAME", "_DATABASE", "_DB", "_DBNAME"} {
		if strings.HasSuffix(u, suf) {
			return true
		}
	}
	return false
}

func serviceIP(built *Built, name string) string {
	for _, s := range built.Services {
		if s.Name == name && s.Spec.ClusterIP != "" && s.Spec.ClusterIP != "None" {
			return s.Spec.ClusterIP
		}
	}
	return ""
}

// seedFamily groups image names that share a seed script. Empty means unsupported.
func seedFamily(engine string) string {
	switch engine {
	case "redis", "valkey":
		return "redis"
	case "postgres", "postgresql":
		return "postgres"
	case "mysql", "mariadb":
		return "mysql"
	case "mongo", "mongodb":
		return "mongo"
	case "kafka":
		return "kafka"
	case "rabbitmq":
		return "rabbitmq"
	default:
		return ""
	}
}

func defaultSeedPort(engine string) int32 {
	switch seedFamily(engine) {
	case "redis":
		return 6379
	case "postgres":
		return 5432
	case "mysql":
		return 3306
	case "mongo":
		return 27017
	case "kafka":
		return 9092
	case "rabbitmq":
		return 5672
	default:
		return 0
	}
}

// seedPorts are the destinations a seed pod may dial for an engine.
func seedPorts(engine string, port int32) []int32 {
	return []int32{port}
}

const seedRedisScript = `set -e
host="$1"; port="$2"; shift 2
i=0
for pair in "$@"; do
  key="${pair%%	*}"; body="${pair#*	}"
  redis-cli -h "$host" -p "$port" SET "$key" "$body" >/dev/null
  i=$((i+1))
done
echo "seeded $i keys" > /dev/termination-log
`

const seedPostgresScript = `set -e
host="$1"; port="$2"; shift 2
export PGHOST="$host" PGPORT="$port" PGUSER="${PGUSER:-postgres}" PGDATABASE="${PGDATABASE:-postgres}" PGSSLMODE="${PGSSLMODE:-disable}"
psql -v ON_ERROR_STOP=1 -c "CREATE TABLE IF NOT EXISTS sentinel_lab_seed (i int PRIMARY KEY, path text, body text);"
i=0
for pair in "$@"; do
  key="${pair%%	*}"; body="${pair#*	}"
  esc=$(printf "%s" "$body" | sed "s/'/''/g")
  path=$(printf "%s" "$key" | sed "s/'/''/g")
  psql -v ON_ERROR_STOP=1 -c "INSERT INTO sentinel_lab_seed(i, path, body) VALUES ($i, '$path', '$esc') ON CONFLICT (i) DO UPDATE SET path = EXCLUDED.path, body = EXCLUDED.body;"
  i=$((i+1))
done
echo "seeded $i rows" > /dev/termination-log
`

const seedMySQLScript = `set -e
host="$1"; port="$2"; shift 2
user="${MYSQL_USER:-${MYSQL_ROOT_USER:-root}}"
if [ -n "${MYSQL_PWD:-${MYSQL_PASSWORD:-${MYSQL_ROOT_PASSWORD:-}}}" ]; then
  export MYSQL_PWD="${MYSQL_PWD:-${MYSQL_PASSWORD:-${MYSQL_ROOT_PASSWORD}}}"
fi
mysql -h "$host" -P "$port" -u "$user" -e "CREATE DATABASE IF NOT EXISTS sentinel; CREATE TABLE IF NOT EXISTS sentinel.sentinel_lab_seed (i INT PRIMARY KEY, path TEXT, body TEXT);"
i=0
for pair in "$@"; do
  key="${pair%%	*}"; body="${pair#*	}"
  esc=$(printf "%s" "$body" | sed "s/'/''/g")
  path=$(printf "%s" "$key" | sed "s/'/''/g")
  mysql -h "$host" -P "$port" -u "$user" -e "INSERT INTO sentinel.sentinel_lab_seed(i, path, body) VALUES ($i, '$path', '$esc') ON DUPLICATE KEY UPDATE path=VALUES(path), body=VALUES(body);"
  i=$((i+1))
done
echo "seeded $i rows" > /dev/termination-log
`

const seedMongoScript = `set -e
host="$1"; port="$2"; shift 2
mongo_eval() {
  if command -v mongosh >/dev/null 2>&1; then
    mongosh --quiet --host "$host" --port "$port" --eval "$1"
  else
    mongo --quiet --host "$host" --port "$port" --eval "$1"
  fi
}
i=0
for pair in "$@"; do
  key="${pair%%	*}"; body="${pair#*	}"
  esc=$(printf "%s" "$body" | sed 's/\\/\\\\/g; s/"/\\"/g')
  path=$(printf "%s" "$key" | sed 's/\\/\\\\/g; s/"/\\"/g')
  mongo_eval "db.getSiblingDB(\"sentinel\").sentinel_lab_seed.updateOne({i:$i},{ \$set:{i:$i,path:\"$path\",body:\"$esc\"} },{upsert:true})"
  i=$((i+1))
done
echo "seeded $i docs" > /dev/termination-log
`

const seedKafkaScript = `set -e
host="$1"; port="$2"; shift 2
produce() {
  if command -v kafka-console-producer >/dev/null 2>&1; then
    kafka-console-producer --bootstrap-server "$host:$port" --topic sentinel-lab-seed --property parse.key=true --property key.separator=$'\t'
  elif command -v kafka-console-producer.sh >/dev/null 2>&1; then
    kafka-console-producer.sh --bootstrap-server "$host:$port" --topic sentinel-lab-seed --property parse.key=true --property key.separator=$'\t'
  else
    echo "kafka-console-producer not found" >&2
    exit 1
  fi
}
i=0
for pair in "$@"; do
  printf "%s\n" "$pair" | produce
  i=$((i+1))
done
echo "seeded $i records" > /dev/termination-log
`

// seedRabbitScript is the shell fallback when SealImage is unset (needs rabbitmqadmin).
const seedRabbitScript = `set -e
host="$1"; port="$2"; shift 2
# Fallback when SealImage is empty: rabbitmqadmin talks to management HTTP (15672).
admin_port=15672
if [ "$port" = "15672" ]; then admin_port="$port"; fi
if ! command -v rabbitmqadmin >/dev/null 2>&1; then
  echo "rabbitmqadmin not found (management image required); prefer SealImage lab-seed-amqp" >&2
  exit 1
fi
rabbitmqadmin -H "$host" -P "$admin_port" declare queue name=sentinel.lab.seed durable=true >/dev/null
i=0
for pair in "$@"; do
  key="${pair%%	*}"; body="${pair#*	}"
  payload="$key	$body"
  rabbitmqadmin -H "$host" -P "$admin_port" publish routing_key=sentinel.lab.seed payload="$payload" >/dev/null
  i=$((i+1))
done
echo "seeded $i messages" > /dev/termination-log
`

func seedScript(engine string) string {
	switch seedFamily(engine) {
	case "redis":
		return seedRedisScript
	case "postgres":
		return seedPostgresScript
	case "mysql":
		return seedMySQLScript
	case "mongo":
		return seedMongoScript
	case "kafka":
		return seedKafkaScript
	case "rabbitmq":
		return seedRabbitScript
	default:
		return ""
	}
}

func seedPairArgs(bodies []seedItem) []string {
	out := make([]string, len(bodies))
	for i, b := range bodies {
		out[i] = b.Key + "\t" + b.Body
	}
	return out
}

func seedPod(ns, name, image, host string, port int32, engine string, bodies []seedItem, env []corev1.EnvVar, sealImage string) *corev1.Pod {
	var args []string
	img := image
	if seedFamily(engine) == "rabbitmq" && sealImage != "" {
		img = sealImage
		args = []string{"/sentinel", "lab-seed-amqp",
			"--host", host, "--port", fmt.Sprint(port), "--queue", seedQueue}
		for _, b := range bodies {
			// Include key in the published payload so captures stay path-keyed.
			args = append(args, b.Key+"\t"+b.Body)
		}
	} else {
		script := seedScript(engine)
		args = []string{"sh", "-c", script, "seed", host, fmt.Sprint(port)}
		args = append(args, seedPairArgs(bodies)...)
	}
	pod := labPod(ns, name, nil, args)
	pod.Spec.Containers[0].Image = img
	pod.Spec.Containers[0].Env = append([]corev1.EnvVar{}, env...)
	pod.Spec.Containers[0].Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
	}
	return pod
}

// seedRedisPod keeps the redis-specific helper for existing tests.
func seedRedisPod(ns, name, image, host string, port int32, bodies []string) *corev1.Pod {
	items := make([]seedItem, len(bodies))
	for i, b := range bodies {
		items[i] = seedItem{Key: seedKeyPrefix + fmt.Sprint(i), Body: b}
	}
	return seedPod(ns, name, image, host, port, "redis", items, nil, "")
}

func (p *Prover) waitPodSucceeded(ctx context.Context, ns, name string, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		pod, err := p.Client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		switch pod.Status.Phase {
		case corev1.PodSucceeded:
			return nil
		case corev1.PodFailed:
			return fmt.Errorf("seed pod %s failed: %s", name, strings.TrimSpace(terminationMessage(pod)))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("seed pod %s did not finish within %s", name, within)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
