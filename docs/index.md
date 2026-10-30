---
page_title: "Provider: Railway"
subcategory: ""
description: |-
    Manages Railway environments through Railway's Infrastructure as Code engine. Each resource is one node of the environment graph, built with railway-go-sdk and applied as a Railway ChangeSet.
---

# Railway Provider

Manages Railway environments through Railway's Infrastructure as Code engine. Each resource is one node of the environment graph, built with `railway-go-sdk` and applied as a Railway ChangeSet.

## Example Usage

Declare the provider under the local name `railway` with the source `tambo-ai/railway-iac-sdk`, so its resources are `railway_service`, `railway_database`, `railway_volume`, `railway_bucket` and so on.

```terraform
terraform {
  required_providers {
    railway = {
      source = "tambo-ai/railway-iac-sdk"
    }
  }
}

# Reads RAILWAY_TOKEN or RAILWAY_PROJECT_TOKEN when no token is set here.
provider "railway" {}
```

## How plans work

Each resource is one node of a Railway environment's IaC graph, addressed by type and name (`service.web`). The provider builds the node with [`railway-go-sdk`](https://github.com/railwayapp/railway-go-sdk), diffs it against the live environment with the same engine `railway config plan` uses, and has Railway preview the result. An apply reads the environment again, replans, and sends that ChangeSet through `environmentApplyChangeSet`.

- A resource's configuration is its whole definition, as a `railway.go` file is for `railway config apply`. A source, build or deploy setting you leave unset is reset to Railway's default, and a variable you leave out is deleted; keep secrets set outside Terraform in `preserve_variables`. Domains and TCP proxies you do not list stay attached.
- A refresh records the changes Railway needs in `live_changes`. A plan shows them leaving, and warns with the full list of changes it will send.
- Creating a resource that already exists fails; import it with `<environment_id>/<type>.<name>`. The first plan after an import lists every configured setting as added and `live_changes` as computed from an empty configuration; the plan's \"Railway changes\" warning is the list that will be applied.
- With `partial` set, resources owned by another partial are refused, as they are for a `railway.go` file that declares `const Partial`.
- Railway meters its API per hour for the whole account or workspace, not per token ([limits](https://docs.railway.com/reference/public-api#rate-limits): 1,000 requests an hour on Hobby, 10,000 on Pro), and the Railway CLI spends from the same budget. A plan costs about five requests per environment plus two per resource; an apply costs about fifteen per changed resource. When Railway asks to wait longer than 30 seconds, the provider stops and reports when the limit resets.
- Railway does not reject a ChangeSet planned against an older configuration, so two applies to the same resource at once leave whichever ran last. Run one apply per environment at a time, as you would `railway config apply`.
- Railway cannot delete a bucket: it accepts the deletion and keeps the bucket. Destroying a `railway_bucket` therefore fails and keeps it in state; use a `removed` block with `destroy = false` to stop managing it. Buckets belong to the project, so a deleted bucket's name stays taken, and creating a bucket under a name the project already holds is refused.
- `railway_project` and `railway_environment` sit outside the graph: change sets work inside one environment and cannot create it. They need an account or workspace token, and destroying either deletes everything in it. A new environment starts empty; with `source_environment_id` Railway copies another environment's services into it, which Terraform does not manage until you import them.
- Railway's IaC creates a service's deploy trigger from `source.github`, so a push to its branch deploys it, gated on `check_suites`. Set `wait_for_deployment` to make an apply wait for the deployment it starts and fail if that deployment fails; `data.railway_deployment` reports a service's latest deployment as of the plan.
- `ephemeral.railway_bucket_credentials` opens a bucket's S3 credentials without writing them to the plan or state, for a write-only attribute or another provider. A service in the same environment should reference the bucket's variables with `variable_references` instead.
- A template database such as `engine = "postgres"` creates its own volume, `<name>-volume`, with a suffix if the name is taken, in Railway's default region; Railway ignores `region` when it creates the database. Destroying the database leaves the volume, as `railway config apply` does. To manage the volume, import it as a `railway_volume` after the database exists.

## Coming from `railway.go`

Every part of `railway-go-sdk` has a counterpart here. The provider builds each resource with the SDK call in the left column.

| `railway-go-sdk` | Provider |
| --- | --- |
| `ProjectNamed`, `Project.Graph` | One resource per node; `environment_id` picks the environment |
| `const Partial` | `partial` in the provider block |
| `ServiceNamed`, `ServiceConfig`, `Service` | `railway_service` |
| `Github`, `Image`, `Template`, `Empty` | `source.github`, `source.image`, `source.template`, `source.empty` |
| `AutoUpdates`, `AutoUpdateWindow`, `AutoUpdates*` | `source.image.auto_updates` |
| `ServiceCdn`, `ServiceCdnHtmlCaching*`, `ServiceCdnPurgeOnDeploy*` | `cdn` |
| `Fn`, `FunctionStartCommand`, `FunctionImages`, `FunctionImagePrefix`, `FunctionSourceMaxBytes` | `railway_function` (`code`, `runtime`, `image`) |
| `Postgres`, `Mysql`, `Redis`, `Mongo`, `Database` | `railway_database` (`engine`) |
| `Volume` | `railway_volume`; mount with `volume_mounts` |
| `Bucket` | `railway_bucket` |
| `Group` | `railway_group`; membership with `group` |
| `Preserve` | `preserve_variables` |
| `Shared` | `shared_variables` |
| `Ref`, `Service.Env`, `Resource.Env`, `Address` | `variable_references` with another resource's `address` |
| `NewContext`, `Context` fields | `data.railway_environment` |
| A resource another file or the dashboard owns | `data.railway_service`, `data.railway_database`, `data.railway_volume`, `data.railway_bucket` |
| `Context.RandomString` | `provider::railway::random_string(environment, label, bytes)` |
| `Context.IsEnvironment(name)` | `data.railway_environment.this.name == name` |

<!-- schema generated by tfplugindocs -->
## Schema

### Optional

- `endpoint` (String) Railway GraphQL endpoint. Defaults to `RAILWAY_GRAPHQL_ENDPOINT`, then `https://backboard.railway.com/graphql/v2`.
- `partial` (String) Named IaC partial that owns the resources this configuration manages, the same as `const Partial` in a `railway.go` file. Leave unset to manage resources as the whole project. A resource owned by another partial is refused.
- `project_token` (String, Sensitive) Railway project token, scoped to one project environment. Defaults to `RAILWAY_PROJECT_TOKEN`. Conflicts with `token`.
- `token` (String, Sensitive) Railway account or workspace token, sent as a bearer token. Defaults to `RAILWAY_TOKEN`. Conflicts with `project_token`.
