# Task 6: GitHub Actions CI/CD (GitOps loop: commit → build → push → manifest bump → Argo sync)

> **Goal:** Learn CI/CD on the real stack by building the front half of a GitOps loop and wiring it to the CD half (Argo CD, already live from Task 5). Target loop: commit → GitHub Actions builds a container image → pushes it to a registry (GHCR) → commits the new image tag into the k8s manifest in git → Argo CD pulls and syncs it to `omni-cluster`. Not toy examples — the real push-vs-pull GitOps pattern.
>
> **Status:** ✅ Complete. `demo-app` (Go stdlib web server + `/metrics`) builds on a GitHub-hosted runner via multi-stage Dockerfile, pushes to GHCR tagged by commit SHA, and a second workflow job commits the SHA into `deployment.yaml`. Argo CD Application syncs it to `omni-cluster` (`Synced`), pod `1/1 Running`, image SHA traceable commit→binary→image→git→pod. Preliminary fix: resolved a pre-existing KEDA CRD sync failure (server-side apply). All git-managed on branch `task06-github-actions`, squash-merged to `main`.

---

## Environment Facts

| Thing | Value |
|---|---|
| Cluster | `omni-cluster` (Omni-managed), k8s 1.36.2 / Talos 1.13.6 |
| kubeconfig context | `omni-cluster` |
| Repo / branch | `example-user/homelab-k8s`, branch `task06-github-actions` |
| Registry | **GHCR** — `ghcr.io/example-user/demo-app` (package **private**) |
| App | `demo-app` — Go stdlib HTTP server, `/` + `/metrics`, listens `:8080` |
| App source (in git) | `src/demo-app/` (`main.go`, `go.mod`, `Dockerfile`) |
| App manifests (in git) | `clusters/omni-cluster/apps/demo-app/` (`deployment.yaml`, `service.yaml`, `servicemonitor.yaml`) |
| Argo Application (in git) | `clusters/omni-cluster/argocd/apps/demo-app.yaml` |
| CI workflow (in git) | `.github/workflows/build-demo-app.yaml` |
| Image tag scheme | immutable `:${{ github.sha }}` (never `latest`) |
| First deployed SHA | `e79b55ac14061a9b2611b4eed3c6160922db0f30` |
| CI→registry auth | built-in `GITHUB_TOKEN` (per-run, auto-expiring, no stored secret) |
| CI→git commit-back auth | built-in `GITHUB_TOKEN` with `contents: write` |
| Argo→repo git auth | **HTTPS/443 + read-only fine-grained PAT** (`repository` Secret) — NOT SSH (VLAN blocks 22) |
| Cluster→GHCR pull auth | `docker-registry` Secret `ghcr-pull` in ns `demo-app` — **classic** PAT, `read:packages` only |

**Network personality note (bit us hard this task):** VLAN blocks outbound UDP and non-443 TCP; allows OpenDNS + TCP/443. Two consequences surfaced here for the FIRST time, because this is the first task where the *cluster itself* (not the Mac) had to reach GitHub: (1) Argo's git fetch over **SSH/22 is blocked** → must use **HTTPS/443**; (2) image pulls are 443 → fine. Everything CI-side runs in GitHub's cloud (not the VLAN), so it was unaffected.

---

## Core concepts (settled up front, before touching anything)

### CI vs CD, and the handoff
- **CI (Continuous Integration)** = source → tested, published artifact. Here: Actions runner builds the image, pushes to GHCR. CI's job ends when a new immutable image exists in the registry. It NEVER touches the cluster.
- **CD (Continuous Delivery)** = declared desired state → running reality. Here: Argo CD reconciles the cluster to match git. CD's job is entirely about the cluster; knows nothing about how the image was built.
- **The handoff = a commit that bumps the image tag in the manifest.** CI's last act is to rewrite `image: …:oldsha` → `…:newsha` in `deployment.yaml` and commit it. That commit is the seam. Argo (already watching git) sees the diff and rolls the Deployment. **CI writes to git; CD reads from git; they never talk directly.**

### workload / controller / one-shot-tool (applied to the loop)
- **Actions runner = one-shot tool** — spins up, runs the job, exits. Nothing persists. Same category as `helm install` / `terraform apply`.
- **Argo CD = controller** — reconcile loop, never exits, self-heals drift at 3am with nobody watching. Same category as the Omni server / KEDA operator.
- **The image = artifact**; the app running from it = **workload**.
- Test that sorts them: *"after it finishes, is something still running and watching?"*

### push vs pull deploy (why not `kubectl apply` from Actions?)
- **Push** = Actions runs `kubectl apply` straight at the cluster. Simpler, synchronous, but CI needs long-lived cluster credentials (fat blast radius), and git↔cluster can silently drift.
- **Pull** = Actions updates git, Argo (inside the cluster) pulls. The cluster credential never leaves the cluster (invert the credential direction — CI never holds a key into the cluster). Git = source of truth, drift self-corrected, every deploy is a revertable commit. Cost: more parts, async deploy (Actions "succeeds" the instant it commits; the app isn't live until Argo syncs — check Argo, not the CI log).
- Chose **pull** — mirrors production GitOps, and Argo was already live.

### image tagging (why `latest` breaks GitOps specifically)
- **Mutable tag** (`latest`): the name stays, the image behind it changes. Push new `:latest`, git still says `:latest` → Argo diffs git vs cluster, sees the SAME string both sides → concludes "no change" → **never deploys.** Silent failure.
- **Immutable tag** (git SHA): each build gets a NEW tag. CI's bump changes the actual string in git → Argo sees a real diff → rolls. **The immutable tag is what makes the git commit meaningful to a reconciler.** Bonus: honest rollback (`git revert` → git names the old SHA → Argo rolls back to that exact image).
- Push-model `kubectl apply` hides this (it re-pulls/restarts every run regardless) — which is why the failure mode only appears once you go GitOps.

### Registry decision: GHCR (not Artifact Registry / Docker Hub)
- GHCR: free, image lives next to code, runner auths with built-in `GITHUB_TOKEN` (no stored cred), pulls over 443 (VLAN-OK). Chosen.
- Artifact Registry: mirrors a probable production environment, but drags in GCP billing + clunkier auth for zero homelab gain.
- Docker Hub: anonymous-pull rate limits bite clusters.
- Framing (Task-4 echo): the registry is the swappable **target**; the CI/CD loop is the **tool** you're learning. Pattern transfers 1:1.

---

## PART 0 (preliminary side-quest) — fix the pre-existing KEDA CRD sync failure

Discovered while verifying the Argo baseline: `keda` Application was `OutOfSync / Progressing`. Wanted a clean CD baseline before adding a sibling Application.

**Diagnosis (the error told the fix):** every KEDA resource `Synced` except `CustomResourceDefinition scaledjobs.keda.sh → OutOfSync`, with condition:
`metadata.annotations: Too long: may not be more than 262144 bytes`.

**Root cause (evolved through 3 sub-causes — same symptom, different cause each time):**
1. **256 KB annotation limit.** Default **client-side apply** stuffs the full applied manifest into a `kubectl.kubernetes.io/last-applied-configuration` annotation. KEDA's `scaledjobs` CRD (huge embedded schema) blows past the API server's 262144-byte annotation ceiling.
2. **The CRD never actually existed.** `kubectl get crd scaledjobs.keda.sh` → `NotFound`; `wc -c` of the annotation → `0`. So it wasn't a *stale* annotation to strip — the CRD had never been created, because the *create itself* fails on the annotation write.
3. **Fix = create it once, server-side, impersonating Argo's field manager**, then let Argo adopt it.

**Fix, two parts:**
```bash
# (a) prevention going forward: add ServerSideApply=true to the Application's syncPolicy
#     clusters/omni-cluster/argocd/apps/keda.yaml:
#       syncPolicy:
#         automated: { prune: true, selfHeal: true }
#         syncOptions:
#           - CreateNamespace=true
#           - ServerSideApply=true
kubectl apply -f clusters/omni-cluster/argocd/apps/keda.yaml   # Application is bootstrapped by hand (no app-of-apps)

# (b) one-time bootstrap of the CRD server-side (SSA never writes the oversized annotation)
helm repo add kedacore https://kedacore.github.io/charts 2>/dev/null; helm repo update kedacore
helm template keda kedacore/keda --version 2.20.1 --include-crds \
  | awk 'BEGIN{RS="\n---\n"} /kind: CustomResourceDefinition/{print "---"; print}' \
  | kubectl apply --server-side --force-conflicts --field-manager=argocd-controller -f -

kubectl -n argocd annotate application keda argocd.argoproj.io/refresh=hard --overwrite
kubectl -n argocd get application keda -w    # -> Synced / Healthy
```
- `--server-side` stores field ownership in `managedFields` (server), never the annotation → dodges the 256 KB wall.
- `--field-manager=argocd-controller` makes the manual apply impersonate Argo's manager, so Argo cleanly *adopts* the CRD on its next sync (one-owner rule at the field level).

**Landed as its own focused branch/PR** (`fix-keda-crd-ssa`), separate from Task 6, keeping the Task-6 PR clean.
**IaC honesty:** only the `ServerSideApply=true` line is in git; the hand `kubectl apply --server-side` is an imperative out-of-band bootstrap the repo can't reproduce — note it (like the Task-5 "first Application bootstrapped by hand").

---

## PART 1 — the app (`src/demo-app/`)

Go **stdlib only** (no external deps → no `go.sum`, nothing to `go mod init`/`tidy`, builds with zero module fetches). Exposes `/` and a **hand-rolled Prometheus `/metrics`** (`http_requests_total`) — deliberately matching the Task-5 deferred "persistent ScaledObject demo" so this app can later get a KEDA ScaledObject.

`src/demo-app/main.go`
```go
package main

import (
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
)

var version = "dev" // overridden at build time via -ldflags "-X main.version=<sha>"
var httpRequests atomic.Int64

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpRequests.Add(1)
		fmt.Fprintf(w, "hello from demo-app, version %s\n", version)
	})
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprint(w, "# HELP http_requests_total Total requests handled by demo-app.\n")
		fmt.Fprint(w, "# TYPE http_requests_total counter\n")
		fmt.Fprintf(w, "http_requests_total %d\n", httpRequests.Load())
	})
	log.Printf("demo-app %s listening on :8080", version)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
```

`src/demo-app/go.mod` (hand-written, no `require` block — no external deps)
```
module github.com/example-user/demo-app

go 1.23
```

`src/demo-app/Dockerfile` — **multi-stage** (the centerpiece lesson)
```dockerfile
# ---- build stage: full Go toolchain (~800MB), thrown away ----
FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-X main.version=${VERSION}" -o /demo-app .

# ---- final stage: just the static binary, no OS, no shell ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /demo-app /demo-app
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/demo-app"]
```
Why it matters:
- **Two `FROM`s = two stages.** Toolchain lives only in `build`; final stage `COPY --from=build` pulls just the compiled binary → final image is a few MB, not ~800 MB. Smaller pulls (matters on the VLAN), smaller attack surface.
- **`CGO_ENABLED=0`** → fully static binary → lets the final stage be `distroless/static` (no shell, no package manager). This is the *building* side of the Task-5 `exec format error` (distroless has no `/bin/sh`, by design).
- **`:nonroot` + `USER nonroot`** → runs unprivileged → passes Talos `baseline` PodSecurity with NO `privileged` label (the well-behaved-workload case, unlike node-exporter/Ceph mons).
- **`ARG VERSION` + `-ldflags "-X main.version=..."`** → bakes the git SHA into the `version` var → the running pod reports which build it is. CI passes `--build-arg VERSION=<sha>`.

**Docker does NOT need Go on your Mac** — the toolchain is inside the `golang:1.23` image. (Irrelevant here anyway: no local Docker → skipped the local build; the runner is the authoritative builder, which is what strict GitOps shops do regardless.)

---

## PART 2 — CI workflow (`.github/workflows/build-demo-app.yaml`)

**Lives at the REPO ROOT**, not in `src/demo-app/`. GitHub only reads `<repo-root>/.github/workflows/`. It *builds from* `src/demo-app/` (via `context:`), but it doesn't *live* there.

```yaml
name: build demo-app

on:
  push:
    branches: [main]
    paths:
      - 'src/demo-app/**'
      - '.github/workflows/build-demo-app.yaml'

permissions:
  contents: write      # needed by the update-manifest job's commit-back
  packages: write      # needed to push to GHCR

jobs:
  build-and-push:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v4
      - name: Log in to GHCR
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - name: Build and push
        uses: docker/build-push-action@v6
        with:
          context: ./src/demo-app
          build-args: VERSION=${{ github.sha }}
          push: true
          tags: ghcr.io/example-user/demo-app:${{ github.sha }}

  update-manifest:
    runs-on: ubuntu-latest
    needs: build-and-push
    steps:
      - name: Checkout
        uses: actions/checkout@v4
      - name: Bump image tag in manifest
        run: |
          sed -i "s|image: ghcr.io/example-user/demo-app:.*|image: ghcr.io/example-user/demo-app:${{ github.sha }}|" \
            clusters/omni-cluster/apps/demo-app/deployment.yaml
      - name: Commit and push the bump
        run: |
          git config user.name  "github-actions[bot]"
          git config user.email "github-actions[bot]@users.noreply.github.com"
          git add clusters/omni-cluster/apps/demo-app/deployment.yaml
          git commit -m "ci: bump demo-app image to ${{ github.sha }}"
          git push
```
Why each block:
- **`paths:` = the loop-guard.** Build fires only on `src/demo-app/**` (or the workflow file). The `update-manifest` job commits to `clusters/…` — a DIFFERENT path — so the bump commit does NOT retrigger the build. No `[skip ci]` hack; the non-overlapping paths ARE the guard.
- **`permissions:`** = least privilege on the auto-provisioned token; `contents` bumped to `write` only because the second job commits back.
- **`secrets.GITHUB_TOKEN`** = the whole point: a per-run, auto-expiring, repo-scoped credential GitHub mints/destroys each run. You never create/store/rotate it.
- **`VERSION=${{ github.sha }}` + `tags: …:${{ github.sha }}`** = immutable-tag concept made real; the SHA is baked into the binary AND used as the tag.
- **`needs: build-and-push`** = ordering: never point git at a tag that failed to build (Argo would deploy a reference to nothing → `ImagePullBackOff`).
- **`uses:` = actions** (reusable steps, version-pinned). `build-push-action` runs the `docker build/push` on the runner (its built-in Docker), so no local Docker needed.
- The `update-manifest` job **only edits a text file and commits** — it never touches the cluster. Push-vs-pull holding.

---

## PART 3 — CD: manifests + Argo Application

App workload manifests under `clusters/omni-cluster/apps/demo-app/`

`deployment.yaml` (image tag is a `PLACEHOLDER`; CI rewrites it — and later gained an `imagePullSecrets`, see Part 5)
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo-app
  namespace: demo-app
  labels: { app: demo-app }
spec:
  replicas: 1
  selector:
    matchLabels: { app: demo-app }
  template:
    metadata:
      labels: { app: demo-app }
    spec:
      imagePullSecrets:
        - name: ghcr-pull          # added in Part 5 (private package)
      containers:
        - name: demo-app
          image: ghcr.io/example-user/demo-app:PLACEHOLDER   # CI bumps to the SHA
          ports:
            - { name: http, containerPort: 8080 }
          resources:
            requests: { cpu: 50m, memory: 32Mi }
            limits:   { memory: 64Mi }
```

`service.yaml`
```yaml
apiVersion: v1
kind: Service
metadata:
  name: demo-app
  namespace: demo-app
  labels: { app: demo-app }
spec:
  selector: { app: demo-app }
  ports:
    - { name: http, port: 80, targetPort: http }
```

`servicemonitor.yaml` (bridge to Task-5 KEDA/Prometheus — tells Prometheus to scrape the app)
```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: demo-app
  namespace: demo-app
  labels: { app: demo-app }
spec:
  selector:
    matchLabels: { app: demo-app }
  endpoints:
    - { port: http, path: /metrics, interval: 15s }
```

Argo Application `clusters/omni-cluster/argocd/apps/demo-app.yaml` (matches `keda.yaml` convention). **NOTE the `repoURL` is HTTPS — see Part 4.**
```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo-app
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example-user/homelab-k8s.git   # HTTPS/443, not SSH — VLAN blocks 22
    targetRevision: main
    path: clusters/omni-cluster/apps/demo-app
  destination:
    server: https://kubernetes.default.svc
    namespace: demo-app
  syncPolicy:
    automated: { prune: true, selfHeal: true }
    syncOptions: [ CreateNamespace=true ]
```
- `path:` watches the workload-manifest dir. The Application CR itself lives in `argocd/apps/` and is `kubectl apply`'d by hand once (no app-of-apps yet).
- `CreateNamespace=true` makes ns `demo-app`. No `privileged` label needed — the image runs `nonroot`, passes `baseline`.
- **No `ServerSideApply=true`** — these manifests are tiny, nowhere near the 256 KB wall. Don't cargo-cult the KEDA fix onto small objects.

Bootstrap the Application by hand (once), after the CI bump has landed a real SHA:
```bash
git checkout main && git pull
grep image: clusters/omni-cluster/apps/demo-app/deployment.yaml   # should show the SHA, not PLACEHOLDER
kubectl apply -f clusters/omni-cluster/argocd/apps/demo-app.yaml
kubectl -n argocd get application demo-app -w
```

---

## PART 4 — CD gotcha #1: Argo git fetch must be HTTPS/443 (not SSH/22)

**Symptom:** demo-app Application `SYNC STATUS: Unknown` (not `OutOfSync`). `Unknown` = Argo could not even COMPARE (couldn't load target state), i.e. it couldn't fetch the repo.

**Condition messages (evolved):**
- First: `ComparisonError … DeadlineExceeded … context deadline exceeded` (repo-server logs showed NO clone attempt — SSH connection black-holing).
- The `repoURL` in the Application was `git@github.com:…` = **SSH over port 22**.

**Root cause:** Argo's `repo-server` runs INSIDE `omni-cluster` (on the locked-down VLAN). The VLAN blocks outbound 22, so `git clone git@github.com:…` hangs → Argo kills it at the deadline. **KEDA worked only because its `repoURL` is a public Helm chart over 443, not the repo over SSH.** This is the FIRST task where the cluster itself had to reach the private repo (prior tasks drove Argo from the Mac, which is off-VLAN).

**Proof (isolate the network layer, like the earlier approach):**
```bash
kubectl -n argocd run nettest --rm -it --restart=Never --image=nicolaka/netshoot -- \
  sh -c 'nc -zv -w5 github.com 22; echo "---"; nc -zv -w5 github.com 443'
#  -> port 22 (tcp) timed out;  443 succeeded!   ← diagnosis nailed
```

**Fix: swap Argo's repo credential to HTTPS + a read-only PAT.**
1. Mint a **fine-grained** PAT: owner `example-user`, only `homelab-k8s`, **Contents: Read-only**. (Read-only, single-repo = least privilege, same as the retired SSH deploy key.)
2. Replace the `repository` Secret (HTTPS form — `username`+`password`, not `sshPrivateKey`):
```bash
# zsh read syntax (bash -p flag does NOT work in zsh — see gotchas)
read -rs "GH_PAT?PAT: "; echo
echo "${#GH_PAT}"                       # VERIFY non-zero before using
kubectl -n argocd delete secret homelab-k8s-repo
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: homelab-k8s-repo
  namespace: argocd
  labels: { argocd.argoproj.io/secret-type: repository }
stringData:
  type: git
  url: https://github.com/example-user/homelab-k8s.git
  username: example-user
  password: ${GH_PAT}
EOF
unset GH_PAT
```
3. Flip the Application `repoURL` to the `https://` form (edit the file; verify the LIVE object changed — a `sed` miss once left the live object on SSH → apply said `unchanged`):
```bash
# after editing the file:
kubectl apply -f clusters/omni-cluster/argocd/apps/demo-app.yaml
kubectl -n argocd get application demo-app -o jsonpath='{.spec.source.repoURL}{"\n"}'   # MUST read https://
kubectl -n argocd annotate application demo-app argocd.argoproj.io/refresh=hard --overwrite
```
- Argo matches credential→repo by EXACT URL string. The `repository` Secret `url:` and the Application `repoURL` must be byte-identical.
- **Correction to the Task-5 mental model:** cluster→repo git is HTTPS/443 + PAT; the SSH deploy key only worked from the Mac. Any future Application on this repo must use the HTTPS URL.

---

## PART 5 — CD gotcha #2: private GHCR package → imagePullSecret

**Symptom:** Argo `Synced`, but pod `ImagePullBackOff`. This is a DIFFERENT credential path than the git fetch (cluster→registry, not Argo→git).

**Read the event (reference vs auth vs network — the Task-5 distinction):**
```bash
kubectl -n demo-app describe pod -l app=demo-app | grep -A6 -i events
```
- First: `401 Unauthorized … failed to fetch anonymous token` = pulling **anonymously**, package is **private**. Need a credential.
- After adding secret: `403 Forbidden` = authenticated but **not permitted** → token scope/type wrong (401 = who are you; 403 = you're not allowed).

**Fix: a `docker-registry` Secret with a CLASSIC PAT scoped `read:packages`.**
- GHCR pull auth needs a **classic** token with **`read:packages`** (fine-grained PATs don't cleanly cover package reads — a fine-grained token here caused the 403).
- Least privilege: check **only** `read:packages` (NOT `write:packages` — CI pushes with its own `GITHUB_TOKEN`).

```bash
# set the token PLAINLY and GATE it before use (see the poisoned-secret gotcha)
GHCR_PAT='ghp_...'                                   # classic token
echo "starts: ${GHCR_PAT:0:4}   length: ${#GHCR_PAT}"   # MUST be: starts ghp_ / length 40
# only if the gate passes:
kubectl -n demo-app delete secret ghcr-pull 2>/dev/null
kubectl -n demo-app create secret docker-registry ghcr-pull \
  --docker-server=ghcr.io --docker-username=example-user --docker-password="$GHCR_PAT"
unset GHCR_PAT
kubectl -n demo-app delete pod -l app=demo-app       # updating a Secret does NOT re-pull — force fresh pods
kubectl -n demo-app get pods -w                       # -> 1/1 Running
```
Wire it in git (only the REFERENCE, never the credential): add `imagePullSecrets: [{name: ghcr-pull}]` to the pod spec (see Part 3), commit, let Argo apply.

**Mental model:** git carries the *reference* to the secret; the cluster holds the secret's *contents*. Same rule as the Argo repo Secret and `*.asc`. Vault (Task 8) automates the contents; this is the honest interim.
**`imagePullSecrets` is namespaced** — the Secret and the Deployment referencing it must both be in `demo-app`.

**Alternative (deferred skill):** flip the package to Public → no imagePullSecret at all. Legit for a secretless demo app; skips learning the mechanism.

---

## TROPHY — prove the SHA end-to-end
```bash
kubectl -n demo-app get pods                          # 1/1 Running
kubectl -n demo-app port-forward svc/demo-app 8080:80 &
sleep 2
curl -s localhost:8080                                # "hello from demo-app, version e79b55ac..."
kubectl -n demo-app get deploy demo-app -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
kill %1
```
Pod-reported SHA == Deployment image tag == the commit CI built from. One hash, traceable: commit → runner builds it (`-ldflags`) → tags the image → commits the tag into git → Argo pulls → pod runs → pod reports the commit. GitOps proven, not asserted.

---

## KEY LESSONS / GOTCHAS (do these right next time)

1. **Same error string, different cause — read the STATE, don't pattern-match the message.** The KEDA `Too long: 262144 bytes` error had 3 distinct causes in sequence (annotation limit → CRD never created → needed server-side bootstrap). The `NotFound` / `wc -c → 0` was the pivot that reframed it. Decode/inspect the actual object; don't keep re-applying the same fix.
2. **client-side apply vs server-side apply.** Client-side stuffs the whole manifest into a `last-applied-configuration` annotation (256 KB cap). Large CRDs (KEDA `scaledjobs`) overflow it. `ServerSideApply=true` tracks field ownership server-side (no annotation, no cap). It PREVENTS future writes but can't create an object that already failed — bootstrap the CRD once with `kubectl apply --server-side --force-conflicts --field-manager=argocd-controller`.
3. **Cluster→GitHub over SSH/22 is blocked by the VLAN — use HTTPS/443.** This is the first task the cluster itself reached the private repo; the Task-5 SSH deploy key only worked from the Mac. Argo `SYNC STATUS: Unknown` = can't fetch/compare. Prove with a `netshoot` pod + `nc -zv github.com 22 / 443`.
4. **`Unknown` ≠ `OutOfSync`.** `OutOfSync` = compared, they differ. `Unknown` = couldn't compare at all (fetch/auth failure). Different diagnosis.
5. **Argo matches credential→repo by EXACT URL string.** `repository` Secret `url:` and Application `repoURL` must be byte-identical (protocol, case, `.git`).
6. **Verify the LIVE object changed, not just the file.** A `sed` miss left the Application on the SSH URL; `kubectl apply` said `unchanged` and Argo kept failing. Always `get … -o jsonpath` the live field after applying.
7. **Private GHCR package → `imagePullSecret` (classic PAT, `read:packages` only).** `401` = anonymous/no cred; `403` = wrong token type/scope. GHCR pull wants a **classic** token; a fine-grained PAT caused the 403.
8. **Updating a Secret does NOT re-pull.** The kubelet caches the last pull result — `kubectl delete pod` to force fresh pods to re-attempt with the corrected Secret.
9. **GATE every secret value before you store it.** `read -rs` in **zsh** silently captured the *prompt text* into the variable → the Secret stored `read -rs "GHCR_PAT?…"` as the password → 403. Set the value plainly and `echo "starts: ${VAR:0:4} length: ${#VAR}"` BEFORE building the secret. Decode the stored Secret (`… | base64 -d`) to see the actual bytes when in doubt.
10. **Know your shell — you're on zsh.** `read -p` fails (`no coprocess`; use `read -rs "VAR?prompt"`). `#` comments pasted alone get executed (`command not found: #`). Bash-isms don't all translate. Same family as `zsh: command not found: docker`.
11. **classic vs fine-grained PATs.** Classic = `ghp_`, 40 chars. Fine-grained = `github_pat_`/`gith…`, ~93 chars. GHCR pull needs classic `read:packages`; git HTTPS clone works with fine-grained `Contents:Read`. The length/prefix check catches a wrong type instantly.
12. **Multi-stage Docker build** = toolchain in `build` stage, only the binary copied to a distroless final stage → tiny image, no shell (the build-side of Task-5's `exec format error`). `CGO_ENABLED=0` for a static binary; `nonroot` user passes Talos `baseline`.
13. **`.github/workflows/` at the REPO ROOT** — GitHub reads nowhere else. It *builds from* `src/demo-app/` via `context:`, but doesn't live there.
14. **stdlib over a client library when a toolchain/network isn't available** — no external deps = no `go.sum` = no `go mod init/tidy` = builds offline. The `/metrics` text exposition format is a simple contract; Prometheus/KEDA can't tell what produced it.
15. **The path-filter loop-guard.** Build triggers on `src/demo-app/**`; the bump commit touches `clusters/**` → no retrigger. Non-overlapping paths, not `[skip ci]`.
16. **GitHub API flakiness happens** — `gh pr create` threw a transient GraphQL 500 (same outage breaking the web UI). Fell back to a pure-git squash merge (no PR record). If the portfolio wants the PR record, open one retroactively when the API recovers.

---

## Git / GitHub workflow used (prod-style)

```
[side-quest] branch fix-keda-crd-ssa -> fix -> apply -> verify Synced -> commit -> PR -> squash-merge -> delete
[task]       branch task06-github-actions -> per-step commits -> push
             -> review own diff (git diff --stat main...HEAD ; full diff)
             -> merge to main (squash) -> the merge FIRES CI -> bot commits the tag bump on main
             -> bootstrap Argo Application by hand -> Argo syncs
```
Key points banked:
- **`main...HEAD` (three dots)** = what this branch changed since it diverged from main (the PR "Files changed" view). Two dots is subtly different.
- **Review = read `--stat` first** — a surprise file in the stat is the highest-value catch. Confirmed only the intended 8 files; keda fix correctly absent (separate branch).
- **Squash merge** collapses the branch to one commit on `main`; that commit touches `src/demo-app/**` → trips the CI trigger. The bot's `ci: bump…` is a SECOND commit on `main` right after — expected, not an error.
- **CLI PR via `gh`:** `gh pr create --base main --head <branch> --title … --body …` then `gh pr merge --squash --delete-branch`. `gh` also streams runs: `gh run watch` (useful when the web UI is flaky). Pure-git fallback (no PR): `git merge --squash <branch>` → commit → push.
- **`git config user.name = github-actions[bot]`** in the CI commit-back makes machine commits visually distinct from human ones in `git lg`.

---

## Conceptual Q&A

**Q: What connects CI and CD?**
A commit that bumps the image tag in the manifest. CI's last act rewrites `image: …:sha` in `deployment.yaml` and commits it; Argo (watching git) diffs and rolls. CI writes to git, CD reads from git — they never talk directly.

**Q: Why not just `kubectl apply` from Actions (push model)?**
It's simpler and synchronous but forces CI to hold long-lived cluster credentials (fat blast radius) and lets git/cluster drift. Pull (Actions→git→Argo) inverts the credential direction — the cluster holds its own key and only reads git — and makes git the source of truth with self-healed drift. Chose pull to mirror production GitOps and because Argo was live.

**Q: Why immutable SHA tags, not `latest`?**
Argo reconciles the TEXT in git. `:latest` never changes in git even when the image behind it does → Argo sees no diff → never deploys (silent). A SHA tag changes the string on every build → real diff → Argo rolls. Also gives honest `git revert` rollbacks.

**Q: Why did KEDA work over git but demo-app didn't?**
KEDA's `repoURL` is a public Helm chart over HTTPS/443. demo-app pointed Argo at the private repo over SSH/22, which the VLAN blocks. First task the cluster itself had to reach the repo.

**Q: 401 vs 403 on an image pull?**
401 = no/unknown credential (was pulling anonymously against a private package). 403 = authenticated but not permitted (wrong token type/scope — a fine-grained PAT instead of a classic `read:packages` one).

**Q: Why does updating the pull Secret not fix the pod by itself?**
The kubelet caches the last pull result. You must delete the pod so a new one re-attempts the pull with the corrected Secret.

**Q: Runner vs Argo — one-shot or controller?**
Runner = one-shot tool (spins up, runs, exits — like `helm install`/`terraform apply`). Argo = controller (reconcile loop, never exits, self-heals — like the Omni server/KEDA operator). Test: "after it finishes, is something still running and watching?"

---

## APPENDIX — Mental Model / Architecture Recap (come back here when the picture goes fuzzy)

After a task heavy on tactical firefighting (CRD annotations, token types, secrets), the *spatial* model can get buried. These are the anchors to re-see it.

### Anchor 1 — inside vs outside the VLAN (the one idea that resolves most confusion)

```
OUTSIDE THE VLAN  (cloud + your Mac)
  [ your Mac ]     [ GitHub: repo / Actions / GHCR ]     [ Cloudflare DNS ]     [ Auth0 ]
   kubectl,git           build + image store              example.com          Omni login
        |                       |
========|=======================|===== ONLY TCP/443 (+ OpenDNS) crosses this line =========
        |                       |
INSIDE THE LOCKED-DOWN VLAN 192.0.2.0/24   (blocks UDP, SSH/22, external DNS resolvers)

  [ Omni host .60 ]     [ dnsmasq .61 ]
   manages clusters      DHCP + int DNS

  omni-cluster  (Omni-managed, k8s 1.36 / Talos 1.13):
     Rook-Ceph          [operator]           distributed storage (sdb disk per worker)
     Prometheus+Grafana [workload]           scrape /metrics, TSDB lives on Ceph
     metrics-server     [workload]           CPU/mem for native HPA
     Argo CD            [controller]         watches git over HTTPS/443, reconciles cluster
     KEDA               [controller+adapter] ScaledObject -> generates an HPA
     demo-app           [workload]           Go app; image pulled from GHCR (Task 6)
```

**Everything you BUILT lives inside the VLAN; everything you DEPEND ON (GitHub, GHCR, Cloudflare, Auth0, your Mac) lives outside.** There is one narrow door through the wall: TCP/443 (+ OpenDNS for names). Every hard problem across Tasks 1/5/7 was the same shape — something inside needed to reach something outside and tried a door that's bolted shut:
- Task 1: NTP (UDP) blocked -> use internal DC NTP; WireGuard (UDP) blocked -> self-host Omni so SideroLink stays on the LAN.
- Task 6: SSH/22 blocked -> Argo git over HTTPS/443; image pulls are 443 -> fine.

**Fix is always the same shape: get onto 443, or stay internal.**

Color/category anchor for omni-cluster: **controllers** = the two things that never stop watching/reconciling (Argo, KEDA). **substrate** = what apps rely on (Ceph storage, Prometheus metrics). **workload** = your actual app (demo-app). Controllers watch-and-correct; everything else runs-and-does-a-job.

### Anchor 2 — the Task 6 loop is just five moves (git is the seam)

```
1. you push code            git push to main                     ┐
2. Actions builds image  -> pushes it to GHCR by SHA             ┘ CI  (one-shot runner: runs, exits)

3. Actions commits the new tag INTO git   <=== THE SEAM (CI writes git here; CD reads git here)

4. Argo sees the git change (reads git)                          ┐
5. Argo deploys it; the node PULLS the image from GHCR, runs pod ┘ CD  (Argo controller: never exits)
```

CI and CD **never talk to each other directly** — git is the mailbox between them. CI's job ends by writing a fact into git; CD's job begins by reading it. Every Task-6 gotcha maps to exactly one box or arrow:
- SSA / CRD saga = pre-loop cleanup (getting Argo healthy first) — not even one of the five moves.
- SSH-vs-443 = the arrow *into* box 4 (Argo reaching git).
- imagePullSecret / token type = the arrow *from GHCR into* box 5 (node pulling the image).
- zsh `read` / poisoned secret = in service of that one pull credential.

### Anchor 3 — what an image / registry / container actually are

```
code + Dockerfile  --build-->  IMAGE (sealed, inert artifact)  --push-->  GHCR (the warehouse)
                                                                               |
                                                                     --pull (later, by a node)-->
                                                                               v
                                                          CONTAINER (image unsealed + running = workload)
```

- **Container image** = a frozen, read-only, layered bundle containing your compiled program + everything it needs to run. Analogy: a sealed, labeled shipping container — pack it once, copy it anywhere, open it and get identical contents every time. Your `demo-app` image is the output of the multi-stage Dockerfile: just the static binary on a distroless base, a few MB. **The image is the artifact** (produced, inert).
- **Container** = what you get when a node *runs* an image — a live process. **One image -> many containers** (same image can back 1 pod or 10). The running app is **the workload**.
- **Registry** = a server whose whole job is to hold images and hand them out. "Store/push" = upload the sealed bundle to it; "pull" = download it to run. The warehouse between "who built it" (runner, ephemeral) and "who runs it" (cluster node).
- **GHCR (GitHub Container Registry)** = GitHub's registry, `ghcr.io`. Your image lives at `ghcr.io/example-user/demo-app` — registry / account / image-name — exactly parallel to how your *code* lives at `github.com/example-user/...`. Reachable over 443 (VLAN-OK); runner auths with the built-in `GITHUB_TOKEN`.
- **Tag** (`...:e79b55ac...`) = the label on one specific sealed bundle. GHCR holds many `demo-app` images, one per build; the tag says which exact one. This is why immutable SHA tags matter — "deploy commit e79b55a" points at one exact bundle forever.

**See it for real:** GitHub profile (`github.com/example-user`) -> **Packages** tab -> `demo-app`. Shows the pull command, `Private` badge (why the pull secret was needed), the **Versions/tags** list (your SHA is there), and the tiny size (multi-stage payoff). CLI: `gh api user/packages/container/demo-app/versions --jq '.[].metadata.container.tags'`.

### Anchor 4 — HPA re-anchor (Task 5 concept, re-cemented)

**HPA (HorizontalPodAutoscaler)** = a built-in Kubernetes **[controller]** (same category as Argo/KEDA, NOT a workload). Its one job: watch a metric, compare to a target, change the *number of pod replicas* to hold the metric near target. "Horizontal" = adds/removes whole pods (scale out/in), never resizes one pod. A thermostat for pod count:

```
desiredReplicas = ceil( currentReplicas × (currentMetric / targetMetric) )
```

HPA is **blind on its own** — it can ONLY read the built-in k8s metrics APIs, not Prometheus/Kafka directly. So the question is always "who feeds it?":
- CPU/mem -> `metrics-server` [workload] fills the resource-metrics API. (native HPA, Task 5 first half)
- anything else -> an adapter fills the external-metrics API. **KEDA is that adapter.**

```
you write a ScaledObject
   -> KEDA generates + OWNS an HPA (you saw keda-hpa-web)
      -> HPA reads a metric, does the ceil() math, sets replica count
         -> metric from metrics-server (CPU) OR KEDA/adapter (Prometheus, etc.)
```

**KEDA drives HPA, it does NOT replace it.** KEDA adds what raw HPA can't: event/app-metric scalers (60+) and scale-to-zero (raw HPA floors at 1). Subtlety: native CPU HPA measures usage as a **% of the pod's `requests`** (not limits, not node capacity) — so right-sizing `requests` IS calibrating the autoscaler.
