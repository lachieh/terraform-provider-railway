// Ported from railwayapp/cli src/iac/tests.rs (MIT, Copyright (c) Railway Corp.)
//
// Not ported, because they exercise code that stays in the CLI: the seven
// evaluates_* tests (language subprocess runners), the three eval_context_*
// tests (EvalContext::to_json) and legacy_runner_only_when_explicitly_requested
// (mod.rs).

package iac

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestEmitsChangeSetWireVersion(t *testing.T) {
	current := graphFrom()
	desired := graphFrom(service("web", nil))
	assertEqual(t, ChangeSetVersion, 1)
	assertEqual(t, diff(current, desired).Version, 1)
	assertEqual(t, GraphVersion, 1)
}

func TestNumericReplicasAreCountOnly(t *testing.T) {
	graph := graphFrom(service("web", obj(`{ "deploy": { "numReplicas": 1 } }`)))
	web := graph.FindResource("service.web")
	assertJSON(t, web["deploy"], js(`{ "numReplicas": 1 }`))
	config := GraphToEnvironmentConfig(graph, CompileOptions{})
	assertJSON(t, at(config, "services", "web", "deploy"), js(`{ "numReplicas": 1 }`))
}

func TestCountOnlyReplicaChangesKeepCurrentRegion(t *testing.T) {
	current := graphFrom(service("web", obj(`{ "deploy": { "multiRegionConfig": { "us-east4": { "numReplicas": 1 } } } }`)))
	desired := graphFrom(service("web", obj(`{ "deploy": { "numReplicas": 2 } }`)))
	changeSet := diff(current, desired)
	assertEqual(t, len(changeSet.Changes), 1)
	assertEqual(t, change(t, changeSet, 0)["kind"], any("resource.update"))
	assertEqual(t, change(t, changeSet, 0)["field"], any("deploy"))
}

func TestDockerfileBuildDefaultsDoNotDrift(t *testing.T) {
	current := graphFrom(service("backend", obj(`{ "build": { "builder": "DOCKERFILE", "dockerfilePath": "Dockerfile", "buildCommand": "" } }`)))
	desired := graphFrom(service("backend", obj(`{ "build": { "builder": "DOCKERFILE" } }`)))
	assertNoChanges(t, diff(current, desired))
}

func TestVolumeUpsizeIsSafeDownsizeIsDestructive(t *testing.T) {
	small := graphFrom(volume("data", js(`{ "sizeMB": 1024 }`)))
	large := graphFrom(volume("data", js(`{ "sizeMB": 2048 }`)))
	assertEqual(t, change(t, diff(small, large), 0)["severity"], any("safe"))
	assertEqual(t, change(t, diff(large, small), 0)["severity"], any("destructive"))
}

func TestSharedVariableReferencesCompile(t *testing.T) {
	graph := graphFrom(service("web", obj(`{
		"variables": {
			"API_KEY": { "type": "sharedReference", "name": "API_KEY" },
			"DASHED": { "type": "sharedReference", "name": "DASHED-KEY" }
		}
	}`)))
	config := GraphToEnvironmentConfig(graph, CompileOptions{})
	assertJSON(t, at(config, "services", "web", "variables"), js(`{
		"API_KEY": { "value": "${{shared.API_KEY}}" },
		"DASHED": { "value": "${{shared.DASHED-KEY}}" }
	}`))
}

func TestDatabaseRegionMapsToServiceAndVolume(t *testing.T) {
	graph := graphFrom(postgres("db", "europe-west4"))
	config := GraphToEnvironmentConfig(graph, CompileOptions{
		ServiceIDsByName:       map[string]any{"db": "service-id"},
		VolumeIDsByServiceName: map[string]any{"db": "volume-id"},
		ExistingServiceIDs:     []string{"service-id"},
	})
	assertJSON(t, at(config, "services", "service-id", "deploy", "multiRegionConfig"), js(`{ "europe-west4": { "numReplicas": 1 } }`))
	assertJSON(t, at(config, "volumes", "volume-id", "region"), "europe-west4")
}

func TestDatabaseRegionChangeIsDestructive(t *testing.T) {
	current := graphFrom(postgres("db", "us-west2"))
	desired := graphFrom(postgres("db", "europe-west4"))
	got := change(t, diff(current, desired), 0)
	assertEqual(t, got["severity"], any("destructive"))
	assertEqual(t, got["summary"], any("Move database db to europe-west4"))
}

func TestImportedDatabaseWithoutExplicitRegionIsClean(t *testing.T) {
	current := EnvironmentConfigToGraph(obj(`{
		"services": {
			"db-id": {
				"source": { "image": "ghcr.io/railwayapp-templates/postgres-ssl:18" },
				"deploy": {
					"multiRegionConfig": { "us-east4-eqdc4a": { "numReplicas": 1 } },
					"requiredMountPath": "/var/lib/postgresql/data"
				},
				"volumeMounts": { "vol-id": { "mountPath": "/var/lib/postgresql/data" } }
			}
		}
	}`), ConfigToGraphOptions{
		ProjectName:            "app",
		ServiceNamesByID:       map[string]any{"db-id": "postgres"},
		TemplateServiceIDsByID: map[string]any{"db-id": "tpl-postgres"},
	})
	desired := graphFrom(postgres("postgres", ""))
	assertNoChanges(t, diff(current, desired))
}

func TestCustomDomainRegistrationIsDiagnosed(t *testing.T) {
	current := envConfig(obj(`{ "services": {} }`))
	desired := graphFrom(service("web", obj(`{ "networking": { "customDomains": { "app.example.com": { "port": 8080 } } } }`)))
	result := diff(current, desired)
	assertTrue(t, !anyChange(result, func(c map[string]any) bool { return c["kind"] == "domain.create" }), "unexpected domain.create")
	assertTrue(t, anyDiagnostic(result, func(d Diagnostic) bool { return strings.Contains(d.Message, "not supported") }), "diagnostics: %+v", result.Diagnostics)
}

func TestExistingCustomDomainsArePlanClean(t *testing.T) {
	current := EnvironmentConfigToGraph(obj(`{ "services": { "web": {} } }`), ConfigToGraphOptions{
		ProjectName:              "app",
		CustomDomainsByServiceID: map[string]any{"web": js(`{ "app.example.com": {} }`)},
	})
	desired := graphFrom(service("web", obj(`{ "networking": { "customDomains": { "app.example.com": {} } } }`)))
	result := diff(current, desired)
	assertNoChanges(t, result)
	assertEqual(t, len(result.Diagnostics), 0)
}

func importedPostgres(networking any) Graph {
	svc := obj(`{
		"source": { "image": "ghcr.io/railwayapp-templates/postgres-ssl:18" },
		"deploy": {
			"multiRegionConfig": { "us-east4-eqdc4a": { "numReplicas": 1 } },
			"requiredMountPath": "/var/lib/postgresql/data"
		},
		"volumeMounts": { "vol-id": { "mountPath": "/var/lib/postgresql/data" } }
	}`)
	if networking != nil {
		svc["networking"] = networking
	}
	return EnvironmentConfigToGraph(map[string]any{"services": map[string]any{"db-id": svc}}, ConfigToGraphOptions{
		ProjectName:            "app",
		ServiceNamesByID:       map[string]any{"db-id": "postgres"},
		TemplateServiceIDsByID: map[string]any{"db-id": "tpl-postgres"},
	})
}

func withNetworking(node map[string]any, networking any) map[string]any {
	node["networking"] = networking
	return node
}

func TestEmptyDatabaseTcpProxiesConvergeWhenNoProxyExists(t *testing.T) {
	current := importedPostgres(nil)
	desired := graphFrom(withNetworking(postgres("postgres", ""), js(`{ "tcpProxies": {} }`)))
	assertNoChanges(t, diff(current, desired))

	current = managedDBConfig(obj(`{
		"services": {
			"cache": {
				"source": { "image": "railwayapp/redis:8.2" },
				"deploy": { "requiredMountPath": "/bitnami" }
			}
		}
	}`))
	desired = graphFrom(withNetworking(redis("cache"), js(`{ "tcpProxies": {} }`)))
	assertNoChanges(t, diff(current, desired))
}

func TestRedisImageWithoutTemplateProvenanceImportsAsService(t *testing.T) {
	current := envConfig(obj(`{ "services": { "cache": { "source": { "image": "redis:7" } } } }`))
	cache := current.Resources[0]
	assertEqual(t, cache["address"], any("service.cache"))
	assertEqual(t, cache["type"], any("service"))
	assertJSON(t, cache["source"], image("redis:7"))

	desired := graphFrom(service("cache", map[string]any{"source": image("redis:7")}))
	assertNoChanges(t, diff(current, desired))
}

func TestRedisImageWithTemplateProvenanceImportsAsDatabase(t *testing.T) {
	current := managedDBConfig(obj(`{ "services": { "cache": { "source": { "image": "railwayapp/redis:8.2" } } } }`))
	cache := current.Resources[0]
	assertEqual(t, cache["address"], any("database.cache"))
	assertEqual(t, cache["engine"], any("redis"))
}

func TestServiceDeclaredOverManagedDatabaseIsNotReplaced(t *testing.T) {
	current := managedDBConfig(obj(`{ "services": { "cache": { "source": { "image": "railwayapp/redis:8.2" } } } }`))
	desired := graphFrom(service("cache", map[string]any{"source": image("railwayapp/redis:8.2")}))
	result := diff(current, desired)
	assertNoChanges(t, result)
	assertEqual(t, len(result.Diagnostics), 1)
	assertEqual(t, result.Diagnostics[0].Severity, "warning")
	assertEqual(t, result.Diagnostics[0].Path, "resources.service.cache")

	// Edits on the mismatched pair still apply as the same service.
	desired = graphFrom(service("cache", map[string]any{
		"source":    image("railwayapp/redis:8.2"),
		"variables": js(`{ "MAXMEMORY": { "type": "literal", "value": "1gb" } }`),
	}))
	result = diff(current, desired)
	assertEqual(t, len(result.Diagnostics), 1)
	assertOnlyVariableSet(t, result, "MAXMEMORY", "service.cache")

	// And the other way round: a plain service declared as database().
	current = envConfig(obj(`{ "services": { "cache": { "source": { "image": "railwayapp/redis:8.2" } } } }`))
	result = diff(current, graphFrom(redis("cache")))
	assertNoChanges(t, result)
	assertEqual(t, len(result.Diagnostics), 1)

	cache := redis("cache")
	cache["variables"] = js(`{ "MAXMEMORY": { "type": "literal", "value": "1gb" } }`)
	result = diff(current, graphFrom(cache))
	assertEqual(t, len(result.Diagnostics), 1)
	assertOnlyVariableSet(t, result, "MAXMEMORY", "database.cache")
}

func assertOnlyVariableSet(t *testing.T, result ChangeSet, variable, address string) {
	t.Helper()
	assertJSON(t, kinds(result), []string{"variable.set"})
	assertEqual(t, result.Changes[0]["variable"], any(variable))
	assertEqual(t, result.Changes[0]["address"], any(address))
}

func TestImportedDatabaseKeepsItsTcpProxy(t *testing.T) {
	current := importedPostgres(js(`{ "tcpProxies": { "5432": {} } }`))
	database := current.FindResource("database.postgres")
	if database == nil {
		t.Fatal("database.postgres not imported")
	}
	assertJSON(t, database["networking"], js(`{ "tcpProxies": { "5432": {} } }`))

	desired := graphFrom(withNetworking(postgres("postgres", ""), js(`{ "tcpProxies": { "5432": {} } }`)))
	assertNoChanges(t, diff(current, desired))
}

func TestUnauthoredDatabaseNetworkingIsNotDrift(t *testing.T) {
	current := importedPostgres(js(`{ "tcpProxies": { "5432": {} } }`))
	desired := graphFrom(postgres("postgres", ""))
	assertNoChanges(t, diff(current, desired))
}

func TestEmptyTcpProxiesWarnInsteadOfPlanningARemovalTheApplySkips(t *testing.T) {
	current := importedPostgres(js(`{ "tcpProxies": { "5432": {} } }`))
	desired := graphFrom(withNetworking(postgres("postgres", ""), js(`{ "tcpProxies": {} }`)))
	changeSet := diff(current, desired)
	assertNoChanges(t, changeSet)
	warning := diagnostic(t, changeSet, 0)
	assertEqual(t, warning.Severity, "warning")
	assertEqual(t, warning.Path, "resources.database.postgres.networking.tcpProxies")
	assertTrue(t, strings.Contains(warning.Message, `{ "5432": null }`), "message: %s", warning.Message)
}

func TestANullTcpProxyEntryPlansTheRemovalAndConverges(t *testing.T) {
	current := importedPostgres(js(`{ "tcpProxies": { "5432": {} } }`))
	desired := graphFrom(withNetworking(postgres("postgres", ""), js(`{ "tcpProxies": { "5432": null } }`)))
	changeSet := diff(current, desired)
	assertJSON(t, kinds(changeSet), []string{"resource.update"})
	got := change(t, changeSet, 0)
	assertEqual(t, got["field"], any("networking"))
	assertEqual(t, got["summary"], any("Update postgres networking"))
	assertJSON(t, got["before"], js(`{ "tcpProxies": { "5432": {} } }`))
	// The apply is sent the block as written, so the `null` entry survives to
	// the server: that entry is what deletes the proxy.
	assertJSON(t, got["after"], js(`{ "tcpProxies": { "5432": null } }`))
	assertEqual(t, len(changeSet.Diagnostics), 0)

	// The proxy is gone; the same file has nothing left to do.
	current = importedPostgres(nil)
	assertNoChanges(t, diff(current, desired))
}

func TestOmittedNetworkingKeysKeepWhatRailwayHas(t *testing.T) {
	current := importedPostgres(js(`{
		"privateNetworkEndpoint": "postgres",
		"tcpProxies": { "5432": {} }
	}`))
	// tcpProxies left out: the apply never touches the proxy.
	desired := graphFrom(withNetworking(postgres("postgres", ""), js(`{ "privateNetworkEndpoint": "postgres" }`)))
	assertNoChanges(t, diff(current, desired))
	// privateNetworkEndpoint left out: the apply never touches the endpoint.
	desired = graphFrom(withNetworking(postgres("postgres", ""), js(`{ "tcpProxies": { "5432": {} } }`)))
	assertNoChanges(t, diff(current, desired))
}

func TestServiceTcpEmptyWarnsAboutAProxyItCannotRemove(t *testing.T) {
	current := envConfig(obj(`{
		"services": {
			"web": {
				"source": { "image": "ghcr.io/acme/web:1.2.3" },
				"networking": { "tcpProxies": { "8080": {} } }
			}
		}
	}`))
	desired := graphFrom(service("web", map[string]any{
		"source":     image("ghcr.io/acme/web:1.2.3"),
		"networking": js(`{ "tcpProxies": {} }`),
	}))
	changeSet := diff(current, desired)
	assertNoChanges(t, changeSet)
	assertEqual(t, diagnostic(t, changeSet, 0).Severity, "warning")
	assertTrue(t, strings.Contains(changeSet.Diagnostics[0].Message, `{ "8080": null }`), "message: %s", changeSet.Diagnostics[0].Message)
}

func TestPublicDatabaseDeclarationPlansTheProxy(t *testing.T) {
	current := importedPostgres(nil)
	desired := graphFrom(withNetworking(postgres("postgres", ""), js(`{ "tcpProxies": { "5432": {} } }`)))
	changeSet := diff(current, desired)
	assertJSON(t, kinds(changeSet), []string{"resource.update"})
	assertJSON(t, changeSet.Changes[0]["after"], js(`{ "tcpProxies": { "5432": {} } }`))
}

func TestEmptyServiceTcpProxiesConvergeWhenNoProxyExists(t *testing.T) {
	current := envConfig(obj(`{ "services": { "web": { "source": { "image": "ghcr.io/acme/web:1.2.3" } } } }`))
	desired := graphFrom(service("web", map[string]any{
		"source":     image("ghcr.io/acme/web:1.2.3"),
		"networking": js(`{ "tcpProxies": {} }`),
	}))
	assertNoChanges(t, diff(current, desired))
}

func TestRoundTrippedConfigPlansNoChanges(t *testing.T) {
	desired := graphFrom(
		service("web", map[string]any{
			"source":    github("railwayapp/demo"),
			"variables": js(`{ "PUBLIC_FLAG": { "type": "literal", "value": "on" } }`),
		}),
		service("worker", map[string]any{"source": image("ghcr.io/acme/worker:1.2.3")}),
	)
	current := EnvironmentConfigToGraph(GraphToEnvironmentConfig(desired, CompileOptions{}), ConfigToGraphOptions{ProjectName: "app"})
	assertNoChanges(t, diff(current, desired))
}

func TestTracingRoundTripsAndFalseSwitchesAreNotDrift(t *testing.T) {
	desired := graphFrom(service("web", map[string]any{
		"source":  image("ghcr.io/acme/web:1"),
		"tracing": js(`{ "enabled": true, "autoInstrumentation": true }`),
	}))
	compiled := GraphToEnvironmentConfig(desired, CompileOptions{})
	assertJSON(t, at(compiled, "services", "web", "tracing"), js(`{ "enabled": true, "autoInstrumentation": true }`))
	current := EnvironmentConfigToGraph(compiled, ConfigToGraphOptions{ProjectName: "app"})
	assertNoChanges(t, diff(current, desired))

	// Railway serialises an untraced service without a block; an authored
	// `{ enabled: false }` means the same thing.
	current = envConfig(obj(`{ "services": { "web": { "source": { "image": "ghcr.io/acme/web:1" } } } }`))
	desired = graphFrom(service("web", map[string]any{
		"source":  image("ghcr.io/acme/web:1"),
		"tracing": js(`{ "enabled": false }`),
	}))
	assertNoChanges(t, diff(current, desired))
}

func TestTracingChangesPlanAnUpdateWithTheRightDeployEffect(t *testing.T) {
	current := envConfig(obj(`{ "services": { "web": { "source": { "image": "ghcr.io/acme/web:1" } } } }`))

	enable := graphFrom(service("web", map[string]any{
		"source":  image("ghcr.io/acme/web:1"),
		"tracing": js(`{ "enabled": true }`),
	}))
	result := diff(current, enable)
	assertJSON(t, kinds(result), []string{"resource.update"})
	assertEqual(t, result.Changes[0]["field"], any("tracing"))
	assertJSON(t, result.Changes[0]["after"], js(`{ "enabled": true }`))
	assertEqual(t, result.Changes[0]["deployEffect"], any("deploy"))

	autoOnly := graphFrom(service("web", map[string]any{
		"source":  image("ghcr.io/acme/web:1"),
		"tracing": js(`{ "autoInstrumentation": true }`),
	}))
	result = diff(current, autoOnly)
	assertJSON(t, kinds(result), []string{"resource.update"})
	assertEqual(t, result.Changes[0]["deployEffect"], any("none"))

	traced := envConfig(obj(`{
		"services": {
			"web": {
				"source": { "image": "ghcr.io/acme/web:1" },
				"tracing": { "enabled": true, "autoInstrumentation": true }
			}
		}
	}`))
	untraced := graphFrom(service("web", map[string]any{"source": image("ghcr.io/acme/web:1")}))
	result = diff(traced, untraced)
	assertJSON(t, kinds(result), []string{"resource.update"})
	assertEqual(t, result.Changes[0]["field"], any("tracing"))
	assertTrue(t, result.Changes[0]["after"] == nil, "after: %v", result.Changes[0]["after"])
	assertEqual(t, result.Changes[0]["deployEffect"], any("deploy"))
}

func TestNewServiceCarriesTracingOnTheCreate(t *testing.T) {
	current := envConfig(obj(`{ "services": {} }`))
	desired := graphFrom(service("web", map[string]any{
		"source":  image("ghcr.io/acme/web:1"),
		"tracing": js(`{ "enabled": true }`),
	}))
	result := diff(current, desired)
	assertJSON(t, kinds(result), []string{"resource.create"})
	assertJSON(t, at(result.Changes[0], "resource", "tracing"), js(`{ "enabled": true }`))
}

func TestCdnRoundTripsAndDefaultKnobsAreNotDrift(t *testing.T) {
	desired := graphFrom(service("web", map[string]any{
		"source": image("ghcr.io/acme/web:1"),
		"cdn":    js(`{ "enabled": true, "htmlCaching": "force" }`),
	}))
	compiled := GraphToEnvironmentConfig(desired, CompileOptions{})
	assertJSON(t, at(compiled, "services", "web", "cdn"), js(`{ "enabled": true, "htmlCaching": "force" }`))
	current := EnvironmentConfigToGraph(compiled, ConfigToGraphOptions{ProjectName: "app"})
	assertNoChanges(t, diff(current, desired))

	// Railway serialises only the knobs that differ from the edge defaults;
	// an authored default means the same thing as leaving it out.
	current = envConfig(obj(`{
		"services": {
			"web": {
				"source": { "image": "ghcr.io/acme/web:1" },
				"cdn": { "enabled": true }
			}
		}
	}`))
	desired = graphFrom(service("web", map[string]any{
		"source": image("ghcr.io/acme/web:1"),
		"cdn": js(`{
			"enabled": true,
			"defaultTtlSeconds": 7200,
			"htmlCaching": "auto",
			"staleWhileRevalidate": true,
			"purgeOnDeploy": "html"
		}`),
	}))
	assertNoChanges(t, diff(current, desired))

	// An uncached service has no block; `enabled: false` means the same
	// thing, whatever other knobs sit beside it.
	current = envConfig(obj(`{ "services": { "web": { "source": { "image": "ghcr.io/acme/web:1" } } } }`))
	desired = graphFrom(service("web", map[string]any{
		"source": image("ghcr.io/acme/web:1"),
		"cdn":    js(`{ "enabled": false, "htmlCaching": "force" }`),
	}))
	assertNoChanges(t, diff(current, desired))
}

func TestCdnChangesPlanAnUpdateWithoutADeploy(t *testing.T) {
	current := envConfig(obj(`{ "services": { "web": { "source": { "image": "ghcr.io/acme/web:1" } } } }`))

	enable := graphFrom(service("web", map[string]any{
		"source": image("ghcr.io/acme/web:1"),
		"cdn":    js(`{ "enabled": true }`),
	}))
	result := diff(current, enable)
	assertJSON(t, kinds(result), []string{"resource.update"})
	assertEqual(t, result.Changes[0]["field"], any("cdn"))
	assertJSON(t, result.Changes[0]["after"], js(`{ "enabled": true }`))
	assertEqual(t, result.Changes[0]["deployEffect"], any("none"))

	cached := envConfig(obj(`{
		"services": {
			"web": {
				"source": { "image": "ghcr.io/acme/web:1" },
				"cdn": { "enabled": true, "purgeOnDeploy": "all" }
			}
		}
	}`))
	retuned := graphFrom(service("web", map[string]any{
		"source": image("ghcr.io/acme/web:1"),
		"cdn":    js(`{ "enabled": true, "purgeOnDeploy": "off", "defaultTtlSeconds": 60 }`),
	}))
	result = diff(cached, retuned)
	assertJSON(t, kinds(result), []string{"resource.update"})
	assertEqual(t, result.Changes[0]["field"], any("cdn"))
	assertEqual(t, result.Changes[0]["deployEffect"], any("none"))
	assertJSON(t, result.Changes[0]["details"], js(`[
		"cdn.defaultTtlSeconds (null → 60)",
		"cdn.purgeOnDeploy (\"all\" → \"off\")"
	]`))

	uncached := graphFrom(service("web", map[string]any{"source": image("ghcr.io/acme/web:1")}))
	result = diff(cached, uncached)
	assertJSON(t, kinds(result), []string{"resource.update"})
	assertEqual(t, result.Changes[0]["field"], any("cdn"))
	assertTrue(t, result.Changes[0]["after"] == nil, "after: %v", result.Changes[0]["after"])
	assertEqual(t, result.Changes[0]["deployEffect"], any("none"))
}

func TestNewServiceCarriesCdnOnTheCreate(t *testing.T) {
	current := envConfig(obj(`{ "services": {} }`))
	desired := graphFrom(service("web", map[string]any{
		"source": image("ghcr.io/acme/web:1"),
		"cdn":    js(`{ "enabled": true, "htmlCaching": "never" }`),
	}))
	result := diff(current, desired)
	assertJSON(t, kinds(result), []string{"resource.create"})
	assertJSON(t, at(result.Changes[0], "resource", "cdn"), js(`{ "enabled": true, "htmlCaching": "never" }`))
}

func TestTemplateDatabaseStartCommandDoesNotChurn(t *testing.T) {
	current := managedDBConfig(obj(`{
		"services": {
			"cache": {
				"source": { "image": "ghcr.io/railwayapp-templates/redis:8" },
				"deploy": {
					"requiredMountPath": "/data",
					"startCommand": "/bin/sh -c \"exec docker-entrypoint.sh redis-server --requirepass $REDIS_PASSWORD\""
				}
			}
		}
	}`))
	desired := graphFrom(redis("cache"))
	assertNoChanges(t, diff(current, desired))
}

func TestInlineVolumeConfigIsHoisted(t *testing.T) {
	graph := ProjectDefinitionToGraph(map[string]any{
		"name": "app",
		"resources": []any{service("web", obj(`{
			"volumeAttachments": {
				"data": {
					"volume": "volume.data",
					"mountPath": "/data",
					"volumeConfig": { "sizeMB": 4096, "region": "europe-west4" }
				}
			}
		}`))},
	})
	vol := graph.FindResource("volume.data")
	if vol == nil {
		t.Fatal("volume.data not hoisted")
	}
	assertJSON(t, vol["config"], js(`{ "sizeMB": 4096, "region": "europe-west4" }`))
	encoded, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	assertTrue(t, !strings.Contains(string(encoded), "volumeConfig"), "graph still carries volumeConfig: %s", encoded)
}

func TestPlatformVolumeAlertsDoNotChurn(t *testing.T) {
	current := EnvironmentConfigToGraph(obj(`{
		"services": {
			"web": {
				"source": { "image": "ghcr.io/acme/api:1.2.3" },
				"volumeMounts": { "vol-1": { "mountPath": "/data" } }
			}
		},
		"volumes": {
			"vol-1": {
				"sizeMB": 1024,
				"region": "us-west2",
				"alerts": { "usage": { "80": {}, "95": {}, "100": {} } },
				"allowOnlineResize": true
			}
		}
	}`), ConfigToGraphOptions{
		ProjectName:     "app",
		VolumeNamesByID: map[string]any{"vol-1": "data"},
	})
	desired := ProjectDefinitionToGraph(map[string]any{
		"name": "app",
		"resources": []any{service("web", map[string]any{
			"source": image("ghcr.io/acme/api:1.2.3"),
			"volumeAttachments": js(`{
				"data": {
					"volume": "volume.data",
					"mountPath": "/data",
					"volumeConfig": { "sizeMB": 1024, "region": "us-west2" }
				}
			}`),
		})},
	})
	assertNoChanges(t, diff(current, desired))
}

func TestAuthoredDatabaseStartCommandStillDiffs(t *testing.T) {
	node := func(start string) Graph {
		db := postgres("db", "")
		db["deploy"] = map[string]any{"startCommand": start}
		return graphFrom(db)
	}
	assertEqual(t, change(t, diff(node("old-start"), node("new-start")), 0)["field"], any("deploy"))
}

func TestNeverDeletesDatabaseRealizedVolume(t *testing.T) {
	current := EnvironmentConfigToGraph(obj(`{
		"services": {
			"db": {
				"source": { "image": "ghcr.io/railwayapp-templates/postgres-ssl:18" },
				"deploy": { "requiredMountPath": "/var/lib/postgresql/data" },
				"volumeMounts": { "vol-1": { "mountPath": "/var/lib/postgresql/data" } }
			}
		},
		"volumes": { "vol-1": { "sizeMB": 50000, "region": "us-west2" } }
	}`), ConfigToGraphOptions{
		ProjectName:            "app",
		VolumeNamesByID:        map[string]any{"vol-1": "postgres-volume"},
		TemplateServiceIDsByID: map[string]any{"db": "tpl-postgres"},
	})
	desired := graphFrom(postgres("db", ""))
	result := diff(current, desired)
	assertTrue(t, !contains(kinds(result), "resource.delete"), "kinds: %v", kinds(result))
}

func TestWarnsInsteadOfDeletingMountedVolume(t *testing.T) {
	current := EnvironmentConfigToGraph(obj(`{
		"services": {
			"web": {
				"source": { "image": "ghcr.io/acme/api:1.2.3" },
				"volumeMounts": { "vol-1": { "mountPath": "/data" } }
			}
		},
		"volumes": { "vol-1": { "sizeMB": 1024, "region": "us-west2" } }
	}`), ConfigToGraphOptions{
		ProjectName:     "app",
		VolumeNamesByID: map[string]any{"vol-1": "data"},
	})
	desired := graphFrom(service("web", map[string]any{"source": image("ghcr.io/acme/api:1.2.3")}))
	result := diff(current, desired)
	assertTrue(t, !contains(kinds(result), "resource.delete"), "kinds: %v", kinds(result))
	assertTrue(t, anyDiagnostic(result, func(d Diagnostic) bool { return strings.Contains(d.Message, "never deleted") }), "diagnostics: %+v", result.Diagnostics)
}

func TestRemovingAServiceIsDestructive(t *testing.T) {
	current := envConfig(obj(`{
		"services": {
			"web": { "source": { "repo": "railwayapp/demo" } },
			"api": { "source": { "repo": "railwayapp/api" } }
		}
	}`))
	desired := graphFrom(service("web", map[string]any{"source": github("railwayapp/demo")}))
	deletion := findChange(diff(current, desired), func(c map[string]any) bool { return c["kind"] == "resource.delete" })
	if deletion == nil {
		t.Fatal("no resource.delete")
	}
	assertEqual(t, deletion["address"], any("service.api"))
	assertEqual(t, deletion["severity"], any("destructive"))
}

func TestDoesNotDeleteResourcesOwnedByAnotherPartial(t *testing.T) {
	current := envConfig(obj(`{
		"services": {
			"api": { "source": { "repo": "acme/api" } },
			"worker": { "source": { "repo": "acme/worker" } }
		}
	}`))
	desired := graphFrom(service("api", map[string]any{"source": github("acme/api")}))
	owners := Partials{"service.api": "api", "service.worker": "worker"}
	result := DiffGraphs(DiffOptions{Current: &current, Desired: &desired, Partial: "api", Owners: owners})
	assertTrue(t, !contains(kinds(result), "resource.delete"), "kinds: %v", kinds(result))
	assertJSON(t, result.Declared, []string{"service.api"})
}

func TestErrorsWhenPartialDeclaresForeignResource(t *testing.T) {
	current := envConfig(obj(`{ "services": { "api": { "source": { "repo": "acme/api" } } } }`))
	desired := graphFrom(service("api", map[string]any{"source": github("acme/api")}))
	owners := Partials{"service.api": "api"}
	result := DiffGraphs(DiffOptions{Current: &current, Desired: &desired, Partial: "worker", Owners: owners})
	assertNoChanges(t, result)
	assertTrue(t, anyDiagnostic(result, func(d Diagnostic) bool {
		return strings.Contains(d.Message, `already managed by partial "api"`)
	}), "diagnostics: %+v", result.Diagnostics)
}

func TestErrorsWhenNamelessFileMeetsNamedPartials(t *testing.T) {
	current := envConfig(obj(`{ "services": { "api": { "source": { "repo": "acme/api" } } } }`))
	desired := graphFrom(service("api", map[string]any{"source": github("acme/api")}))
	owners := Partials{"service.api": "api"}
	result := DiffGraphs(DiffOptions{Current: &current, Desired: &desired, Owners: owners})
	assertTrue(t, anyDiagnostic(result, func(d Diagnostic) bool { return d.Path == "partial" }), "diagnostics: %+v", result.Diagnostics)
}

func TestWholeProjectOwnerStillDeletes(t *testing.T) {
	current := envConfig(obj(`{
		"services": {
			"web": { "source": { "repo": "railwayapp/demo" } },
			"api": { "source": { "repo": "railwayapp/api" } }
		}
	}`))
	desired := graphFrom(service("web", map[string]any{"source": github("railwayapp/demo")}))
	owners := Partials{"service.web": "*", "service.api": "*"}
	result := DiffGraphs(DiffOptions{Current: &current, Desired: &desired, Owners: owners})
	assertTrue(t, anyChange(result, func(c map[string]any) bool {
		return c["address"] == "service.api" && c["kind"] == "resource.delete"
	}), "changes: %s", StableStringify(result.Changes))
}

func TestDeletesAPartialsOwnOmittedService(t *testing.T) {
	current := envConfig(obj(`{
		"services": {
			"api": { "source": { "repo": "acme/api" } },
			"extra": { "source": { "repo": "acme/extra" } }
		}
	}`))
	desired := graphFrom(service("api", map[string]any{"source": github("acme/api")}))
	owners := Partials{"service.api": "api", "service.extra": "api"}
	result := DiffGraphs(DiffOptions{Current: &current, Desired: &desired, Partial: "api", Owners: owners})
	assertTrue(t, anyChange(result, func(c map[string]any) bool {
		return c["address"] == "service.extra" && c["kind"] == "resource.delete"
	}), "changes: %s", StableStringify(result.Changes))
}

func TestVariableRemovalIsDestructive(t *testing.T) {
	current := envConfig(obj(`{
		"services": { "web": { "source": { "repo": "r" }, "variables": { "OLD": { "value": "1" } } } }
	}`))
	desired := graphFrom(service("web", map[string]any{
		"source":    github("r"),
		"variables": js(`{ "NEW": { "type": "literal", "value": "2" } }`),
	}))
	result := diff(current, desired)
	assertTrue(t, anyChange(result, func(c map[string]any) bool {
		return c["kind"] == "variable.delete" && c["variable"] == "OLD" && c["severity"] == "destructive"
	}), "changes: %s", StableStringify(result.Changes))
	assertTrue(t, anyChange(result, func(c map[string]any) bool {
		return c["kind"] == "variable.set" && c["variable"] == "NEW" && c["severity"] == "safe"
	}), "changes: %s", StableStringify(result.Changes))
}

func TestImportedVariablesPreserveSealedAndInlineDecrypted(t *testing.T) {
	graph := envConfig(obj(`{
		"services": {
			"web": {
				"source": { "repo": "r" },
				"variables": {
					"PUBLIC_URL": { "value": "https://example.com", "isSealed": false },
					"SECRET": { "value": "should-not-leak", "isSealed": true },
					"MASKED": { "value": "" }
				}
			}
		}
	}`))
	web := graph.FindResource("service.web")
	vars := web["variables"]
	assertEqual(t, at(vars, "PUBLIC_URL", "type"), any("literal"))
	assertEqual(t, at(vars, "PUBLIC_URL", "value"), any("https://example.com"))
	assertEqual(t, at(vars, "SECRET", "type"), any("preserve"))
	assertEqual(t, at(vars, "MASKED", "type"), any("preserve"))
}

func TestPreserveVariableNeverPlans(t *testing.T) {
	current := envConfig(obj(`{
		"services": { "web": { "source": { "repo": "r" }, "variables": { "SECRET": { "value": "existing" } } } }
	}`))
	desired := graphFrom(service("web", map[string]any{
		"source":    github("r"),
		"variables": js(`{ "SECRET": { "type": "preserve" } }`),
	}))
	for _, c := range diff(current, desired).Changes {
		assertTrue(t, !strings.HasPrefix(FieldStr(c, "kind"), "variable"), "unexpected %s", StableStringify(c))
	}
}

func graphWithPolicy(policy any, resources ...map[string]any) Graph {
	return ProjectDefinitionToGraph(map[string]any{
		"name":      "app",
		"variables": policy,
		"resources": toValues(resources),
	})
}

func variableOps(changeSet ChangeSet) []string {
	ops := []string{}
	for _, c := range changeSet.Changes {
		kind := FieldStr(c, "kind")
		if !strings.HasPrefix(kind, "variable") {
			continue
		}
		ops = append(ops, fmt.Sprintf("%s %s:%s", kind, FieldStr(c, "address"), FieldStr(c, "variable")))
	}
	return ops
}

func TestIgnorePatternsAreRemovedFromBothSidesBeforeDiff(t *testing.T) {
	current := envConfig(obj(`{
		"services": {
			"web": { "source": { "repo": "r" }, "variables": {
				"DOPPLER_TOKEN": { "value": "a" },
				"doppler_token": { "value": "case" },
				"NOT_DOPPLER_TOKEN": { "value": "full" },
				"ONLY_API": { "value": "web" },
				"KEEP": { "value": "1" }
			}},
			"api": { "source": { "repo": "r" }, "variables": {
				"DOPPLER_TOKEN": { "value": "b" },
				"ONLY_API": { "value": "api" }
			}}
		}
	}`))
	desired := ProjectDefinitionToGraph(NormalizeProject(map[string]any{
		"name":      "app",
		"variables": js(`{ "managed": true, "ignore": ["DOPPLER_*", "api/ONLY_*"] }`),
		"resources": []any{
			service("web", map[string]any{
				"source": github("r"),
				"env": js(`{
					"KEEP": { "type": "literal", "value": "1" },
					"DOPPLER_NEW": { "type": "literal", "value": "nope" },
					"PUBLIC": "on"
				}`),
			}),
			service("api", map[string]any{"source": github("r"), "variables": map[string]any{}}),
		},
	}))
	assertTrue(t, !desired.Variables.Default, "policy should not be the default")
	assertJSON(t, desired.Variables.Ignore, []string{"DOPPLER_*", "api/ONLY_*"})
	ops := variableOps(diff(current, desired))
	assertTrue(t, !slices.ContainsFunc(ops, func(op string) bool {
		return strings.HasSuffix(op, ":DOPPLER_TOKEN") || strings.HasSuffix(op, ":DOPPLER_NEW")
	}), "ops: %v", ops)
	assertTrue(t, !contains(ops, "variable.delete service.api:ONLY_API"), "ops: %v", ops)
	assertTrue(t, contains(ops, "variable.delete service.web:ONLY_API"), "ops: %v", ops)
	assertTrue(t, contains(ops, "variable.delete service.web:doppler_token"), "ops: %v", ops)
	assertTrue(t, contains(ops, "variable.delete service.web:NOT_DOPPLER_TOKEN"), "ops: %v", ops)
	assertTrue(t, contains(ops, "variable.set service.web:PUBLIC"), "ops: %v", ops)
	assertTrue(t, !slices.ContainsFunc(ops, func(op string) bool { return strings.Contains(op, "KEEP") }), "ops: %v", ops)
}

func TestUnmanagedVariablesPlanNoSetsOrDeletes(t *testing.T) {
	current := envConfig(obj(`{
		"services": {
			"web": { "source": { "repo": "r" }, "variables": { "A": { "value": "1" }, "B": { "value": "2" } } },
			"quiet": { "source": { "repo": "r" }, "variables": { "C": { "value": "3" } } }
		}
	}`))
	desired := graphWithPolicy(
		js(`{ "managed": false, "ignore": [] }`),
		service("web", map[string]any{
			"source": github("r"),
			"variables": js(`{
				"A": { "type": "literal", "value": "changed" },
				"NEW": { "type": "literal", "value": "x" }
			}`),
		}),
		service("quiet", map[string]any{"source": github("r")}),
	)
	result := diff(current, desired)
	assertEqual(t, len(variableOps(result)), 0)
	var errors []Diagnostic
	for _, d := range result.Diagnostics {
		if d.Severity == "error" {
			errors = append(errors, d)
		}
	}
	assertEqual(t, len(errors), 1)
	assertEqual(t, errors[0].Path, "resources.service.web.variables")
	assertEqual(t, errors[0].Message, "Variables are unmanaged (project variables.managed is false). Remove env from web or set managed: true.")
}

func TestOmittedEnvErrorsInsteadOfDeletingExplicitEmptyStaysAuthoritative(t *testing.T) {
	current := managedDBConfig(obj(`{
		"services": {
			"web": { "source": { "repo": "r" }, "variables": { "B": { "value": "2" }, "A": { "value": "1" } } },
			"api": { "source": { "repo": "r" }, "variables": { "OLD": { "value": "1" }, "KEEP": { "value": "1" } } },
			"quiet": { "source": { "repo": "r" }, "variables": { "DOPPLER_TOKEN": { "value": "x" } } },
			"pg": {
				"source": { "image": "ghcr.io/railwayapp-templates/postgres-ssl:18" },
				"variables": { "POSTGRES_PASSWORD": { "value": "s" }, "CUSTOM": { "value": "x" } }
			}
		}
	}`))
	pg := current.FindResource("database.pg")
	if pg == nil {
		t.Fatal("database.pg not imported")
	}
	_, hasVariables := pg["variables"]
	assertTrue(t, !hasVariables, "pg should carry no variables")
	desired := graphWithPolicy(
		js(`{ "managed": true, "ignore": ["DOPPLER_*"] }`),
		service("web", map[string]any{"source": github("r")}),
		service("api", map[string]any{"source": github("r"), "env": map[string]any{}}),
		service("quiet", map[string]any{"source": github("r")}),
		postgres("pg", ""),
	)
	result := diff(current, desired)
	ops := variableOps(result)
	assertTrue(t, contains(ops, "variable.delete service.api:OLD"), "ops: %v", ops)
	assertTrue(t, contains(ops, "variable.delete service.api:KEEP"), "ops: %v", ops)
	for _, op := range ops {
		assertTrue(t, !strings.Contains(op, "service.web"), "ops: %v", ops)
		assertTrue(t, !strings.Contains(op, "service.quiet"), "ops: %v", ops)
		assertTrue(t, !strings.Contains(op, "pg"), "ops: %v", ops)
	}
	var web *Diagnostic
	for index := range result.Diagnostics {
		if result.Diagnostics[index].Path == "resources.service.web.variables" {
			web = &result.Diagnostics[index]
			break
		}
	}
	if web == nil {
		t.Fatalf("no web diagnostic: %+v", result.Diagnostics)
	}
	assertEqual(t, web.Severity, "error")
	assertEqual(t, web.Message, "web declares no env but Railway has 2 variables (A, B). Declare them, mark them preserve(), or add them to variables.ignore.")
	for _, d := range result.Diagnostics {
		assertTrue(t, d.Path != "resources.service.quiet.variables", "diagnostics: %+v", result.Diagnostics)
		assertTrue(t, !strings.Contains(d.Path, "database.pg"), "diagnostics: %+v", result.Diagnostics)
	}
}

func TestDeclaredIgnoredKeyIsAnErrorAndNotSet(t *testing.T) {
	current := envConfig(obj(`{ "services": { "web": { "source": { "repo": "r" } } } }`))
	desired := graphWithPolicy(
		js(`{ "managed": true, "ignore": ["DOPPLER_*", "web/SECRET"] }`),
		service("web", map[string]any{
			"source": github("r"),
			"variables": js(`{
				"DOPPLER_TOKEN": { "type": "literal", "value": "x" },
				"SECRET": { "type": "literal", "value": "y" },
				"OK": { "type": "literal", "value": "z" }
			}`),
		}),
	)
	result := diff(current, desired)
	ops := variableOps(result)
	for _, op := range ops {
		assertTrue(t, !strings.Contains(op, "DOPPLER") && !strings.Contains(op, "SECRET"), "ops: %v", ops)
	}
	assertTrue(t, contains(ops, "variable.set service.web:OK"), "ops: %v", ops)
	var messages []string
	for _, d := range result.Diagnostics {
		messages = append(messages, d.Message)
	}
	assertTrue(t, contains(messages, `web.DOPPLER_TOKEN is declared and also ignored by "DOPPLER_*".`), "messages: %v", messages)
	assertTrue(t, contains(messages, `web.SECRET is declared and also ignored by "web/SECRET".`), "messages: %v", messages)
}

func TestMalformedIgnorePatternsError(t *testing.T) {
	current := envConfig(obj(`{ "services": { "web": { "source": { "repo": "r" } } } }`))
	desired := graphWithPolicy(
		js(`{ "managed": true, "ignore": ["", "a/b/c", "web/", "ghost/*"] }`),
		service("web", map[string]any{"source": github("r"), "variables": map[string]any{}}),
	)
	var errors []string
	for _, d := range diff(current, desired).Diagnostics {
		if d.Severity == "error" {
			errors = append(errors, d.Message)
		}
	}
	assertTrue(t, contains(errors, "variables.ignore contains an empty pattern."), "errors: %v", errors)
	assertTrue(t, slices.ContainsFunc(errors, func(m string) bool {
		return strings.Contains(m, "a/b/c") && strings.Contains(m, "more than one")
	}), "errors: %v", errors)
	assertTrue(t, slices.ContainsFunc(errors, func(m string) bool {
		return strings.Contains(m, "web/") && strings.Contains(m, "empty key")
	}), "errors: %v", errors)
	assertTrue(t, slices.ContainsFunc(errors, func(m string) bool {
		return strings.Contains(m, "ghost") && strings.Contains(m, "neither in the file nor on Railway")
	}), "errors: %v", errors)
}

func TestUnusedIgnorePatternWarns(t *testing.T) {
	current := envConfig(obj(`{
		"services": { "web": { "source": { "repo": "r" }, "variables": {
			"KEEP": { "value": "1" },
			"OTHER": { "value": "1" }
		} } }
	}`))
	desired := graphWithPolicy(
		js(`{ "managed": true, "ignore": ["UNUSED_*", "KEEP"] }`),
		service("web", map[string]any{
			"source":    github("r"),
			"variables": js(`{ "OTHER": { "type": "preserve" } }`),
		}),
	)
	result := diff(current, desired)
	var warnings []Diagnostic
	for _, d := range result.Diagnostics {
		if d.Severity == "warning" {
			warnings = append(warnings, d)
		}
	}
	assertEqual(t, len(warnings), 1)
	assertEqual(t, warnings[0].Message, `variables.ignore pattern "UNUSED_*" matches no variable.`)
	for _, d := range result.Diagnostics {
		assertTrue(t, d.Severity != "error", "diagnostics: %+v", result.Diagnostics)
	}
	assertEqual(t, len(variableOps(result)), 0)
}

func TestVariablePolicyReportAndHeaderLine(t *testing.T) {
	bare := graphFrom(service("web", nil))
	assertTrue(t, bare.Variables.Default && bare.Variables.Managed && len(bare.Variables.Ignore) == 0, "bare policy: %+v", bare.Variables)
	current := envConfig(obj(`{
		"services": {
			"web": { "source": { "repo": "r" }, "variables": {
				"DOPPLER_A": { "value": "1" },
				"DOPPLER_B": { "value": "2" },
				"OTHER": { "value": "3" }
			}},
			"metabase": { "source": { "repo": "r" }, "variables": {
				"MB_DB": { "value": "1" },
				"SITE": { "value": "1" }
			}}
		}
	}`))
	defaultReport := VariablePolicyReport(&current, &bare)
	assertEqual(t, FormatVariablePolicyLine(defaultReport, 5), "variables: managed (default)")

	desired := graphWithPolicy(
		js(`{ "managed": true, "ignore": ["DOPPLER_*", "metabase/*"] }`),
		service("web", map[string]any{
			"source":    github("r"),
			"variables": js(`{ "OTHER": { "type": "preserve" } }`),
		}),
		service("metabase", map[string]any{
			"source": github("r"),
			"variables": js(`{
				"MB_DB": { "type": "preserve" },
				"SITE": { "type": "preserve" }
			}`),
		}),
	)
	report := VariablePolicyReport(&current, &desired)
	assertTrue(t, !report.Default, "report should not be the default")
	assertEqual(t, report.IgnoredCount, 4)
	assertEqual(t, report.ByPattern["DOPPLER_*"], 2)
	assertEqual(t, report.ByPattern["metabase/*"], 2)
	assertEqual(t, FormatVariablePolicyLine(report, 5), "variables: managed · 4 ignored on Railway (DOPPLER_* 2, metabase/* 2)")
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	assertJSON(t, SortedKeys(value), []string{"byPattern", "default", "ignore", "ignoredCount", "managed"})

	explicit := graphWithPolicy(js(`{ "managed": true, "ignore": [] }`), service("web", map[string]any{"source": github("r")}))
	assertEqual(t, FormatVariablePolicyLine(VariablePolicyReport(&current, &explicit), 5), "variables: managed")

	unmanaged := graphWithPolicy(js(`{ "managed": false }`), service("web", map[string]any{"source": github("r")}))
	assertEqual(t, FormatVariablePolicyLine(VariablePolicyReport(&current, &unmanaged), 5), "variables: unmanaged · 5 on Railway not managed")
}

func TestRedactsVariableValuesInPlanOutput(t *testing.T) {
	secret := "sk-super-secret-value-123"
	current := envConfig(obj(`{ "services": { "web": { "source": { "repo": "r" } } } }`))
	desired := graphFrom(service("web", map[string]any{
		"source":    github("r"),
		"variables": map[string]any{"API_KEY": map[string]any{"type": "literal", "value": secret}},
	}))
	changeSet := diff(current, desired)
	apiKey := findChange(changeSet, func(c map[string]any) bool { return c["variable"] == "API_KEY" })
	if apiKey == nil {
		t.Fatal("no API_KEY change")
	}
	rendered := RenderChangeSet(changeSet) + StableStringify(apiKey["summary"]) + StableStringify(apiKey["details"])
	assertTrue(t, !strings.Contains(rendered, secret), "rendered leaks the secret: %s", rendered)
	assertTrue(t, strings.Contains(rendered, "«hidden»"), "rendered: %s", rendered)
	assertEqual(t, at(apiKey, "after", "value"), any(secret))
}

func TestRevealValuesPrintsSecrets(t *testing.T) {
	secret := "sk-super-secret-value-123"
	current := envConfig(obj(`{ "services": { "web": { "source": { "repo": "r" } } } }`))
	desired := graphFrom(service("web", map[string]any{
		"source":    github("r"),
		"variables": map[string]any{"API_KEY": map[string]any{"type": "literal", "value": secret}},
	}))
	changeSet := DiffGraphs(DiffOptions{Current: &current, Desired: &desired, RevealValues: true})
	details, _ := at(change(t, changeSet, 0), "details", 0).(string)
	assertTrue(t, strings.Contains(details, secret), "details: %s", details)
	assertTrue(t, !strings.Contains(details, "«hidden»"), "details: %s", details)
}

func TestRegistryCredentialsFirstApplyPlansAndRedacts(t *testing.T) {
	password := "hunter2-secret"
	current := envConfig(obj(`{ "services": { "web": { "source": { "image": "ghcr.io/acme/api:1.2.3" } } } }`))
	desired := graphFrom(service("web", map[string]any{
		"source": image("ghcr.io/acme/api:1.2.3"),
		"deploy": map[string]any{"registryCredentials": map[string]any{"username": "robot", "password": password}},
	}))
	changes := diff(current, desired)
	assertEqual(t, change(t, changes, 0)["field"], any("deploy"))
	assertTrue(t, !strings.Contains(StableStringify(changes.Changes[0]["details"]), password), "details leak the password")
}

func TestMaskedRegistryCredentialsDoNotChurn(t *testing.T) {
	current := envConfig(obj(`{
		"services": { "web": {
			"source": { "image": "ghcr.io/acme/api:1.2.3" },
			"deploy": { "registryCredentials": { "username": "*****", "password": "*****" } }
		} }
	}`))
	desired := graphFrom(service("web", map[string]any{
		"source": image("ghcr.io/acme/api:1.2.3"),
		"deploy": js(`{ "registryCredentials": { "username": "robot", "password": "hunter2-secret" } }`),
	}))
	assertNoChanges(t, diff(current, desired))
}

func TestNullRegistryCredentialsDoNotChurn(t *testing.T) {
	current := envConfig(obj(`{
		"services": { "web": {
			"source": { "image": "ghcr.io/acme/api:1.2.3" },
			"deploy": { "registryCredentials": null }
		} }
	}`))
	desired := graphFrom(service("web", map[string]any{
		"source": image("ghcr.io/acme/api:1.2.3"),
		"deploy": js(`{ "registryCredentials": { "username": "robot", "password": "hunter2-secret" } }`),
	}))
	assertNoChanges(t, diff(current, desired))
}

func TestOmittingCredentialsDoesNotPlanRemoval(t *testing.T) {
	current := envConfig(obj(`{
		"services": { "web": {
			"source": { "image": "ghcr.io/acme/api:1.2.3" },
			"deploy": { "registryCredentials": { "username": "*****", "password": "*****" } }
		} }
	}`))
	desired := graphFrom(service("web", map[string]any{"source": image("ghcr.io/acme/api:1.2.3")}))
	assertNoChanges(t, diff(current, desired))
}

func TestDecryptedCredentialRotationPlansWithoutLeaking(t *testing.T) {
	password := "hunter2-secret"
	current := envConfig(obj(`{
		"services": { "web": {
			"source": { "image": "ghcr.io/acme/api:1.2.3" },
			"deploy": { "registryCredentials": { "username": "robot", "password": "old-password" } }
		} }
	}`))
	desired := graphFrom(service("web", map[string]any{
		"source": image("ghcr.io/acme/api:1.2.3"),
		"deploy": map[string]any{"registryCredentials": map[string]any{"username": "robot", "password": password}},
	}))
	first := change(t, diff(current, desired), 0)
	rendered := StableStringify(first["summary"]) + StableStringify(first["details"])
	assertTrue(t, strings.Contains(rendered, "registryCredentials"), "rendered: %s", rendered)
	assertTrue(t, !strings.Contains(rendered, password), "rendered leaks the password: %s", rendered)
	assertTrue(t, !strings.Contains(rendered, "old-password"), "rendered leaks the old password: %s", rendered)
}

func TestRotateOneFieldWhenRemoteIsMasked(t *testing.T) {
	password := "hunter2-secret"
	current := envConfig(obj(`{
		"services": { "web": {
			"source": { "image": "ghcr.io/acme/api:1.2.3" },
			"deploy": { "registryCredentials": { "username": "*****", "password": "*****" } }
		} }
	}`))
	desired := graphFrom(service("web", map[string]any{
		"source": image("ghcr.io/acme/api:1.2.3"),
		"deploy": map[string]any{"registryCredentials": map[string]any{"username": "*****", "password": password}},
	}))
	assertJSON(t, change(t, diff(current, desired), 0)["after"], map[string]any{"registryCredentials": map[string]any{"password": password}})
}

func TestImageToGithubClearsCredentials(t *testing.T) {
	current := envConfig(obj(`{
		"services": { "web": {
			"source": { "image": "ghcr.io/acme/api:1.2.3" },
			"deploy": { "registryCredentials": { "username": "*****", "password": "*****" } }
		} }
	}`))
	desired := graphFrom(service("web", map[string]any{"source": github("railwayapp/api")}))
	result := diff(current, desired)
	assertTrue(t, anyChange(result, func(c map[string]any) bool { return c["field"] == "source" }), "changes: %s", StableStringify(result.Changes))
	assertTrue(t, anyChange(result, func(c map[string]any) bool {
		return c["field"] == "deploy" && sameJSON(c["after"], js(`{ "registryCredentials": null }`))
	}), "changes: %s", StableStringify(result.Changes))
}

func TestStaleAutoUpdatesOnGithubSourceDoNotDrift(t *testing.T) {
	current := envConfig(obj(`{
		"services": { "web": { "source": { "repo": "acme/api", "branch": "main", "autoUpdates": { "type": "patch", "schedule": [] } } } }
	}`))
	desired := graphFrom(service("web", map[string]any{"source": github("acme/api")}))
	assertNoChanges(t, diff(current, desired))
}

func TestRefusesRepoConfigOwnedService(t *testing.T) {
	current := envConfig(obj(`{
		"services": { "web": { "source": { "repo": "r" }, "configFile": "railway.json" } }
	}`))
	desired := graphFrom(service("web", map[string]any{"source": github("r")}))
	result := diff(current, desired)
	assertNoChanges(t, result)
	assertTrue(t, anyDiagnostic(result, func(d Diagnostic) bool { return strings.Contains(d.Message, "railway.json") }), "diagnostics: %+v", result.Diagnostics)
}

func TestRestoresVolumeGroupMembership(t *testing.T) {
	graph := EnvironmentConfigToGraph(obj(`{
		"groups": { "group-id": { "name": "Storage" } },
		"volumes": { "volume-id": { "region": "us-west2", "sizeMB": 1024 } }
	}`), ConfigToGraphOptions{
		ProjectName:        "app",
		VolumeNamesByID:    map[string]any{"volume-id": "data"},
		VolumeGroupIDsByID: map[string]any{"volume-id": "group-id"},
	})
	data := graph.FindResource("volume.data")
	assertTrue(t, data != nil && data["groupId"] == "Storage", "resources: %s", StableStringify(graph.Resources))
}

func TestOmitsUnreferencedCanvasGroups(t *testing.T) {
	graph := envConfig(obj(`{
		"groups": {
			"production": { "name": "Production" },
			"test": { "name": "Test-only Sandbox" }
		},
		"services": { "web": { "source": { "image": "nginx:latest" }, "groupId": "production" } }
	}`))
	assertTrue(t, graph.FindResource("group.Production") != nil, "resources: %s", StableStringify(graph.Resources))
	assertTrue(t, graph.FindResource("group.Test-only Sandbox") == nil, "resources: %s", StableStringify(graph.Resources))
}

func TestBucketCreationRequiresRegion(t *testing.T) {
	current := graphFrom()
	for _, resource := range []string{
		`{ "type": "bucket", "name": "assets" }`,
		`{ "type": "bucket", "name": "assets", "config": {} }`,
		`{ "type": "bucket", "name": "assets", "config": { "region": null } }`,
	} {
		t.Run(resource, func(t *testing.T) {
			result := diff(current, graphFrom(obj(resource)))
			assertTrue(t, anyDiagnostic(result, func(d Diagnostic) bool {
				return d.Severity == "error" &&
					d.Path == "resources.bucket.assets.config.region" &&
					strings.Contains(d.Message, "missing a region")
			}), "expected a missing-region diagnostic, got %+v", result.Diagnostics)
		})
	}
}

func TestBucketCreationRejectsInvalidRegions(t *testing.T) {
	current := graphFrom()
	for _, region := range []string{`""`, `"auto"`, `"us-east4-eqdc4a"`, `"IAD"`, `" iad "`, `42`, `false`, `[]`, `{ "region": "iad" }`} {
		t.Run(region, func(t *testing.T) {
			desired := graphFrom(map[string]any{"type": "bucket", "name": "assets", "config": map[string]any{"region": js(region)}})
			result := diff(current, desired)
			assertTrue(t, anyDiagnostic(result, func(d Diagnostic) bool {
				return d.Severity == "error" &&
					d.Path == "resources.bucket.assets.config.region" &&
					strings.Contains(d.Message, "sjc, iad, ams, sin")
			}), "expected an invalid-region diagnostic for %s, got %+v", region, result.Diagnostics)
		})
	}
}

func TestBucketCreationAcceptsStorageRegions(t *testing.T) {
	current := graphFrom()
	for _, region := range []string{"sjc", "iad", "ams", "sin"} {
		t.Run(region, func(t *testing.T) {
			result := diff(current, graphFrom(bucket("assets", region)))
			assertEqual(t, len(result.Diagnostics), 0)
			assertJSON(t, kinds(result), []string{"resource.create"})
		})
	}
}

func TestBucketCreationErrorRemainsWithACoCreatedService(t *testing.T) {
	current := graphFrom()
	desired := graphFrom(
		obj(`{ "type": "bucket", "name": "assets" }`),
		service("web", map[string]any{"source": image("nginx:latest")}),
	)
	result := diff(current, desired)
	assertTrue(t, anyDiagnostic(result, func(d Diagnostic) bool {
		return d.Severity == "error" && d.Path == "resources.bucket.assets.config.region"
	}), "diagnostics: %+v", result.Diagnostics)
	assertTrue(t, anyChange(result, func(c map[string]any) bool {
		return c["kind"] == "resource.create" && c["address"] == "service.web"
	}), "changes: %s", StableStringify(result.Changes))
}

func TestBucketExistingUnchangedRegionDoesNotNeedCreateValidation(t *testing.T) {
	current := envConfig(obj(`{ "buckets": { "assets": { "region": "legacy-region" } } }`))
	desired := graphFrom(bucket("assets", "legacy-region"))
	result := diff(current, desired)
	assertEqual(t, len(result.Diagnostics), 0)
	assertNoChanges(t, result)
}

func TestBucketRegionChangeIsAnError(t *testing.T) {
	current := envConfig(obj(`{ "buckets": { "assets": { "region": "sjc" } } }`))
	desired := graphFrom(bucket("assets", "ams"))
	result := diff(current, desired)
	assertTrue(t, anyDiagnostic(result, func(d Diagnostic) bool { return strings.Contains(d.Message, "region") }), "diagnostics: %+v", result.Diagnostics)
}

func TestUnknownProjectKeyIsAnError(t *testing.T) {
	diagnostics := UnknownFieldDiagnostics(obj(`{ "name": "app", "notAField": true, "resources": [] }`))
	assertEqual(t, len(diagnostics), 1)
	assertEqual(t, diagnostics[0].Severity, "error")
	assertEqual(t, diagnostics[0].Path, "project.notAField")
	assertEqual(t, diagnostics[0].Message, `Unknown field "notAField" on project. This version of terraform-provider-railway-iac-sdk does not support it; upgrade the provider.`)
}

func TestUnknownResourceKeyIsAnError(t *testing.T) {
	diagnostics := UnknownFieldDiagnostics(obj(`{
		"name": "app",
		"resources": [{
			"address": "service.web",
			"type": "service",
			"name": "web",
			"notAField": true
		}]
	}`))
	assertEqual(t, len(diagnostics), 1)
	assertEqual(t, diagnostics[0].Severity, "error")
	assertEqual(t, diagnostics[0].Path, "resources.service.web.notAField")
	assertEqual(t, diagnostics[0].Message, `Unknown field "notAField" on service.web. This version of terraform-provider-railway-iac-sdk does not support it; upgrade the provider.`)
}

func TestAcceptsEveryKnownProjectAndResourceKey(t *testing.T) {
	project := map[string]any{}
	for _, key := range IACProjectFields {
		project[key] = map[string]any{}
	}
	resource := map[string]any{}
	for _, key := range IACResourceFields {
		resource[key] = map[string]any{}
	}
	resource["address"] = "service.web"
	resource["type"] = "service"
	resource["name"] = "web"
	project["resources"] = []any{resource}
	diagnostics := UnknownFieldDiagnostics(project)
	assertTrue(t, len(diagnostics) == 0, "%+v", diagnostics)

	fixtures := map[string]any{
		"name":         "app",
		"environments": js(`["production"]`),
		"variables":    js(`{ "managed": false }`),
		"resources": []any{
			service("web", map[string]any{
				"source":    github("acme/web"),
				"start":     "npm start",
				"build":     "npm run build",
				"variables": js(`{ "PORT": { "type": "literal", "value": "8080" } }`),
				"volumeAttachments": js(`{
					"data": {
						"volume": "volume.data",
						"mountPath": "/data",
						"volumeConfig": { "sizeMB": 1024 }
					}
				}`),
			}),
			postgres("db", "us-west2"),
			volume("data", js(`{ "sizeMB": 1024, "region": "us-west2" }`)),
			bucket("assets", "sjc"),
			obj(`{
				"address": "group.api",
				"type": "group",
				"name": "api",
				"color": "blue",
				"icon": "box",
				"isCollapsed": false
			}`),
		},
	}
	diagnostics = UnknownFieldDiagnostics(fixtures)
	assertTrue(t, len(diagnostics) == 0, "%+v", diagnostics)
}

func githubSource(repo, branch, root string) map[string]any {
	source := map[string]any{"type": "github", "repo": repo}
	if branch != "" {
		source["branch"] = branch
	}
	if root != "" {
		source["rootDirectory"] = root
	}
	return source
}

func diffFollowing(current, desired Graph, follow *BranchFollow) ChangeSet {
	return DiffGraphsFollowing(DiffOptions{Current: &current, Desired: &desired}, follow)
}

func TestBranchlessDesiredSourceDoesNotChangeARemoteBranch(t *testing.T) {
	current := graphFrom(service("web", map[string]any{"source": githubSource("acme/api", "develop", "")}))
	desired := graphFrom(service("web", map[string]any{"source": githubSource("acme/api", "", "")}))
	assertNoChanges(t, diff(current, desired))
}

func TestBranchlessSourceChangeOmitsTheBranchKey(t *testing.T) {
	current := graphFrom(service("web", map[string]any{"source": githubSource("acme/api", "develop", "web")}))
	desired := graphFrom(service("web", map[string]any{"source": githubSource("acme/api", "", "api")}))
	source := findChange(diff(current, desired), func(c map[string]any) bool { return c["field"] == "source" })
	if source == nil {
		t.Fatal("no source change")
	}
	_, hasBranch := get(source["after"], "branch")
	assertTrue(t, !hasBranch, "after: %s", StableStringify(source["after"]))
	assertTrue(t, !strings.Contains(StableStringify(source["after"]), "branch"), "after: %s", StableStringify(source["after"]))
	assertEqual(t, at(source, "after", "rootDirectory"), any("api"))
}

func TestNewBranchlessServicePlansTheRepoDefaultBranch(t *testing.T) {
	follow := &BranchFollow{DefaultBranches: map[string]string{"acme/api": "main"}}
	current := graphFrom()
	desired := graphFrom(service("web", map[string]any{"source": githubSource("acme/api", "", "")}))
	changeSet := diffFollowing(current, desired, follow)
	assertEqual(t, len(changeSet.Diagnostics), 0)
	create := change(t, changeSet, 0)
	assertEqual(t, create["kind"], any("resource.create"))
	assertEqual(t, at(create, "resource", "source", "branch"), any("main"))
	assertEqual(t, at(create, "details", 0), any("branch: main (repo default)"))
	assertEqual(t, create["summary"], any("Create service web (branch: main (repo default))"))
}

func TestNewBranchlessServiceInAPrEnvironmentUsesThePrBranch(t *testing.T) {
	follow := &BranchFollow{
		InPREnvironment: true,
		PRBranch:        "feat/login",
		PRRepo:          "Acme/API",
		DefaultBranches: map[string]string{"acme/api": "main", "other/lib": "trunk"},
	}
	current := graphFrom()
	desired := graphFrom(
		service("web", map[string]any{"source": githubSource("acme/api", "", "")}),
		service("lib", map[string]any{"source": githubSource("other/lib", "", "")}),
	)
	changeSet := diffFollowing(current, desired, follow)
	assertEqual(t, len(changeSet.Diagnostics), 0)
	web := findChange(changeSet, func(c map[string]any) bool { return c["address"] == "service.web" })
	lib := findChange(changeSet, func(c map[string]any) bool { return c["address"] == "service.lib" })
	if web == nil || lib == nil {
		t.Fatalf("changes: %s", StableStringify(changeSet.Changes))
	}
	assertEqual(t, at(web, "resource", "source", "branch"), any("feat/login"))
	assertEqual(t, at(web, "details", 0), any("branch: feat/login (PR branch)"))
	assertEqual(t, web["summary"], any("Create service web (branch: feat/login (PR branch))"))
	assertEqual(t, at(lib, "resource", "source", "branch"), any("trunk"))
	assertEqual(t, at(lib, "details", 0), any("branch: trunk (repo default)"))
}

func TestNewBranchlessServiceErrorsWhenTheDefaultBranchIsUnknown(t *testing.T) {
	follow := &BranchFollow{DefaultBranches: map[string]string{}}
	current := graphFrom()
	desired := graphFrom(service("web", map[string]any{"source": githubSource("acme/api", "", "")}))
	changeSet := diffFollowing(current, desired, follow)
	assertNoChanges(t, changeSet)
	assertEqual(t, diagnostic(t, changeSet, 0).Message, "web is new and its github() source has no branch. Set branch for the first deploy; it can be removed afterwards.")
}

func TestPrEnvironmentRejectsAPinThatDiffersFromTheDeployedBranch(t *testing.T) {
	follow := &BranchFollow{InPREnvironment: true, PRBranch: "feat/login", PRRepo: "acme/api", DefaultBranches: map[string]string{}}
	current := graphFrom(service("web", map[string]any{"source": githubSource("acme/api", "feat/login", "")}))
	desired := graphFrom(service("web", map[string]any{"source": githubSource("acme/api", "main", "")}))
	changeSet := diffFollowing(current, desired, follow)
	assertEqual(t, diagnostic(t, changeSet, 0).Message, `web pins branch "main" but this PR environment deploys "feat/login". Remove branch to follow the environment.`)
}

func TestPrEnvironmentAllowsAnotherRepoToChangeBranch(t *testing.T) {
	follow := &BranchFollow{InPREnvironment: true, PRBranch: "feat/login", PRRepo: "acme/api", DefaultBranches: map[string]string{}}
	current := graphFrom(service("lib", map[string]any{"source": githubSource("other/lib", "main", "")}))
	desired := graphFrom(service("lib", map[string]any{"source": githubSource("other/lib", "develop", "")}))
	changeSet := diffFollowing(current, desired, follow)
	assertEqual(t, len(changeSet.Diagnostics), 0)
	assertTrue(t, anyChange(changeSet, func(c map[string]any) bool { return c["field"] == "source" }), "changes: %s", StableStringify(changeSet.Changes))
}

func TestPrEnvironmentAllowsAPinMatchingTheDeployedBranch(t *testing.T) {
	follow := &BranchFollow{InPREnvironment: true, PRBranch: "feat/login", PRRepo: "acme/api", DefaultBranches: map[string]string{}}
	current := graphFrom(service("web", map[string]any{"source": githubSource("acme/api", "feat/login", "")}))
	desired := graphFrom(service("web", map[string]any{"source": githubSource("acme/api", "feat/login", "")}))
	changeSet := diffFollowing(current, desired, follow)
	assertEqual(t, len(changeSet.Diagnostics), 0)
	assertNoChanges(t, changeSet)
}

func functionService(name, code string) map[string]any {
	return service(name, map[string]any{
		"source": image("ghcr.io/railwayapp/function:1.4.0"),
		"deploy": map[string]any{"startCommand": EncodeFunctionCode(code)},
	})
}

func TestLiveFunctionImageServicesAreKindFunction(t *testing.T) {
	graph := EnvironmentConfigToGraph(map[string]any{
		"services": map[string]any{
			"hello": map[string]any{
				"source": map[string]any{"image": "ghcr.io/railwayapp/function-bun:1.4.0"},
				"deploy": map[string]any{"startCommand": EncodeFunctionCode("export default {}\n")},
			},
			"api": obj(`{ "source": { "image": "ghcr.io/acme/api:1" } }`),
		},
	}, ConfigToGraphOptions{ProjectName: "app"})
	kindOf := func(name string) any {
		for _, resource := range graph.Resources {
			if resource["name"] == name {
				return resource["kind"]
			}
		}
		return nil
	}
	assertEqual(t, kindOf("hello"), any("function"))
	assertEqual(t, kindOf("api"), any("docker-image"))
}

func TestFunctionCodeChangesPlanASourceDiffInsteadOfBase64(t *testing.T) {
	current := graphFrom(functionService("hello", "const a = 1;\nconst b = 2;\n"))
	desiredNode := functionService("hello", "const a = 1;\nconst b = 3;\n")
	desiredNode["kind"] = "function"
	desired := graphFrom(desiredNode)

	result := diff(current, desired)
	assertTrue(t, len(result.Diagnostics) == 0, "%+v", result.Diagnostics)
	assertEqual(t, len(result.Changes), 1)
	got := result.Changes[0]
	assertEqual(t, got["field"], any("deploy"))
	assertEqual(t, got["summary"], any("Update hello source code"))
	assertJSON(t, got["details"], []string{
		"source code (+1 -1)",
		"  const a = 1;",
		"- const b = 2;",
		"+ const b = 3;",
	})
	assertTrue(t, !strings.Contains(StableStringify(got["details"]), "run.sh"), "details: %s", StableStringify(got["details"]))
}

func TestDroppingAFunctionStartCommandWarnsAndReportsTheRemoval(t *testing.T) {
	current := graphFrom(functionService("hello", "line 1\nline 2\n"))
	withoutCode := service("hello", map[string]any{"source": image("ghcr.io/railwayapp/function:1.4.0")})
	withoutCode["kind"] = "function"
	desired := graphFrom(withoutCode)

	result := diff(current, desired)
	assertEqual(t, len(result.Changes), 1)
	assertEqual(t, result.Changes[0]["summary"], any("Update hello source code"))
	assertJSON(t, result.Changes[0]["details"], []string{"source code (2 lines, removed)"})
	assertEqual(t, len(result.Diagnostics), 1)
	warning := result.Diagnostics[0]
	assertEqual(t, warning.Severity, "warning")
	assertEqual(t, warning.Path, "resources.service.hello.deploy.startCommand")
	assertTrue(t, strings.Contains(warning.Message, "declared without code"), "message: %s", warning.Message)
	// The SDK field is `codeFile` (`code_file` in Python), not `code`.
	assertTrue(t, strings.Contains(warning.Message, "Pass `codeFile` (`code_file` in Python) to fn()"), "message: %s", warning.Message)
	assertTrue(t, !strings.Contains(warning.Message, "Pass `code` "), "message: %s", warning.Message)
}

func TestCreatingAFunctionReportsItsSourceSize(t *testing.T) {
	current := graphFrom()
	desired := graphFrom(functionService("hello", "a\nb\nc\n"))
	result := diff(current, desired)
	assertEqual(t, change(t, result, 0)["kind"], any("resource.create"))
	assertJSON(t, result.Changes[0]["details"], []string{"source code (3 lines)"})
}

func TestUnchangedFunctionCodeWithOtherDeployEditsKeepsPlainDetails(t *testing.T) {
	currentNode := functionService("hello", "same\n")
	currentNode["deploy"].(map[string]any)["healthcheckPath"] = "/old"
	desiredNode := functionService("hello", "same\n")
	desiredNode["deploy"].(map[string]any)["healthcheckPath"] = "/new"
	result := diff(graphFrom(currentNode), graphFrom(desiredNode))
	assertEqual(t, len(result.Changes), 1)
	assertJSON(t, result.Changes[0]["details"], []string{`deploy.healthcheckPath ("/old" → "/new")`})
}

func TestPreDeployTimeoutRoundTripsThroughPlanAndApply(t *testing.T) {
	// `deploy.preDeployTimeoutSeconds` has no special casing anywhere in the
	// engine: it rides along inside the opaque `deploy` block. Pin that so a
	// future allowlist or normaliser cannot drop it silently.
	current := envConfig(obj(`{
		"services": {
			"web": {
				"source": { "image": "ghcr.io/acme/web:1" },
				"deploy": { "preDeployCommand": ["npm run migrate"] }
			}
		}
	}`))
	desired := graphFrom(service("web", map[string]any{
		"source": image("ghcr.io/acme/web:1"),
		"deploy": js(`{ "preDeployCommand": ["npm run migrate"], "preDeployTimeoutSeconds": 600 }`),
	}))

	changeSet := diff(current, desired)
	assertJSON(t, kinds(changeSet), []string{"resource.update"})
	got := changeSet.Changes[0]
	assertEqual(t, got["field"], any("deploy"))
	assertJSON(t, at(got, "after", "preDeployTimeoutSeconds"), js(`600`))
	assertEqual(t, got["deployEffect"], any("deploy"))
	details, _ := got["details"].([]any)
	assertEqual(t, len(details), 1)
	detail, _ := details[0].(string)
	assertTrue(t, strings.HasPrefix(detail, "deploy.preDeployTimeoutSeconds"), "unexpected detail: %s", detail)

	// Converged state plans nothing.
	converged := envConfig(obj(`{
		"services": {
			"web": {
				"source": { "image": "ghcr.io/acme/web:1" },
				"deploy": { "preDeployCommand": ["npm run migrate"], "preDeployTimeoutSeconds": 600 }
			}
		}
	}`))
	assertNoChanges(t, diff(converged, desired))

	// Compiling the desired graph back to an environment config keeps it too.
	config := GraphToEnvironmentConfig(desired, CompileOptions{})
	assertJSON(t, at(config, "services", "web", "deploy", "preDeployTimeoutSeconds"), js(`600`))
}
