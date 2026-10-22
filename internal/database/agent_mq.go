package database

import (
	"context"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/sugaryiiy/magpiedb/internal/agent"
)

func mqKind(c Config) string {
	external := mapValue(c.Raw["external_config"])
	kind := str(external, "systemKind", "system_kind")
	if kind == "" {
		kind = c.DriverProfile
	}
	if kind == "" || kind == "native" || kind == "mq" {
		kind = "pulsar"
	}
	return kind
}
func agentPackageKey(c Config) (string, bool) {
	if c.Type == "mq" {
		return agent.Key("mq", mqKind(c))
	}
	return agent.Key(c.Type, c.DriverProfile)
}
func mqAgentConnectParams(c Config) map[string]any {
	external := mapValue(c.Raw["external_config"])
	extra := mapValue(external["extra"])
	auth := mapValue(external["auth"])
	username, password := str(auth, "username"), str(auth, "password")
	if username == "" {
		username, password = c.Username, c.Password
	}
	timeout := c.QueryTimeout * 1000
	if timeout == 0 {
		timeout = 30000
	}
	tlsSkip := boolean(external, "tlsSkipVerify", "tls_skip_verify")
	properties := mapValue(extra["properties"])
	p := map[string]any{"tls_skip_verify": tlsSkip, "request_timeout_ms": timeout, "connect_timeout_ms": c.ConnectTimeout * 1000, "properties": properties}
	if c.ConnectTimeout <= 0 {
		p["connect_timeout_ms"] = 15000
	}
	switch mqKind(c) {
	case "kafka":
		servers := str(extra, "bootstrapServers", "bootstrap_servers")
		if servers == "" {
			servers = c.Host
			if c.Port != 0 && !strings.Contains(servers, ":") {
				servers = net.JoinHostPort(servers, strconv.Itoa(c.Port))
			}
		}
		mechanism := str(extra, "saslMechanism", "sasl_mechanism")
		if mechanism == "" && username != "" {
			mechanism = "PLAIN"
		}
		protocol := str(extra, "securityProtocol", "security_protocol")
		if protocol == "" || protocol == "auto" {
			protocol = "PLAINTEXT"
			if mechanism != "" {
				protocol = "SASL_PLAINTEXT"
			}
			if c.SSL {
				protocol = "SSL"
				if mechanism != "" {
					protocol = "SASL_SSL"
				}
			}
		}
		p["bootstrap_servers"], p["zookeeper_connect_string"] = servers, str(extra, "zookeeperServers", "zookeeper_servers")
		p["sasl_mechanism"], p["security_protocol"] = mechanism, protocol
		p["sasl_username"], p["sasl_password"] = username, password
		if value := str(extra, "saslUsername"); value != "" {
			p["sasl_username"] = value
		}
		if value := str(extra, "saslPassword"); value != "" {
			p["sasl_password"] = value
		}
		p["tls"] = map[string]any{"skip_verify": tlsSkip}
	case "rabbitmq":
		addresses := str(extra, "addresses")
		if addresses == "" {
			addresses = c.Host
		}
		p["addresses"], p["port"] = addresses, integer(extra, 5672, "port")
		if c.Port != 0 {
			p["port"] = c.Port
		}
		p["virtual_host"] = str(extra, "virtualHost", "virtual_host")
		if p["virtual_host"] == "" {
			p["virtual_host"] = "/"
		}
		p["username"], p["password"] = username, password
		if value := str(extra, "username"); value != "" {
			p["username"] = value
		}
		if value := str(extra, "password"); value != "" {
			p["password"] = value
		}
		if value := str(external, "adminUrl", "admin_url"); value != "" {
			p["management_url"] = value
		}
	case "rocketmq":
		namesrv := str(extra, "namesrvAddr", "namesrv_addr")
		if namesrv == "" {
			namesrv = c.Host
		}
		p["namesrv_addr"], p["cluster_name"], p["broker_addr"] = namesrv, str(extra, "clusterName", "cluster_name"), str(extra, "brokerAddr", "broker_addr")
		p["access_key"], p["secret_key"] = username, password
		if value := str(extra, "accessKey", "access_key"); value != "" {
			p["access_key"] = value
		}
		if value := str(extra, "secretKey", "secret_key"); value != "" {
			p["secret_key"] = value
		}
	}
	return p
}
func (d *agentDriver) mqCluster(ctx context.Context) (map[string]any, error) {
	var raw map[string]any
	if err := d.session.Call(ctx, "test_connection", agentConnectParams(d.config), &raw); err != nil {
		return nil, err
	}
	kind := mqKind(d.config)
	caps := map[string]any{}
	for _, field := range []string{"Tenants", "Namespaces", "PartitionedTopics", "Subscriptions", "CreateSubscription", "ResetCursor", "SkipMessages", "ClearBacklog", "PeekMessages", "ExpireMessages", "RateLimits", "BacklogQuota", "Retention", "Permissions", "GeoReplication", "TokenManagement", "RawAdminApi", "SendMessage", "MessageQuery", "Dlq", "MessageTrace", "Exchanges", "ClientConnections", "UserPermissions", "Policies", "ClusterMonitoring"} {
		caps["supports"+field] = false
	}
	for _, field := range []string{"Subscriptions", "ClearBacklog", "PeekMessages", "SendMessage"} {
		caps["supports"+field] = true
	}
	if kind == "rabbitmq" {
		for _, field := range []string{"Namespaces", "Exchanges", "ClientConnections", "UserPermissions", "Policies", "ClusterMonitoring"} {
			caps["supports"+field] = true
		}
	} else {
		for _, field := range []string{"PartitionedTopics", "ResetCursor", "Retention"} {
			caps["supports"+field] = true
		}
		caps["supportsPermissions"] = false
	}
	if kind == "rocketmq" {
		caps["supportsRetention"] = false
		caps["supportsMessageQuery"], caps["supportsMessageTrace"] = true, true
	}
	return map[string]any{"systemKind": kind, "serverVersion": raw["serverVersion"], "resolvedProfile": kind + "-agent", "versionDetection": "agent", "capabilities": caps, "extra": raw}, nil
}
func mqEnvelope(value any, key string) ([]any, error) {
	if items, ok := value.([]any); ok {
		return items, nil
	}
	object := mapValue(value)
	items, ok := object[key].([]any)
	if !ok {
		return nil, errors.New("MQ agent response lacks " + key + " array")
	}
	return items, nil
}
func normalizeMQItems(items []any, kind, list string) []any {
	result := make([]any, 0, len(items))
	for _, raw := range items {
		item := mapValue(raw)
		copy := map[string]any{}
		for k, v := range item {
			copy[k] = v
		}
		if v, ok := copy["vhost"]; ok {
			copy["namespace"] = v
		}
		if list == "topics" {
			copy["shortName"] = item["name"]
			copy["persistent"] = true
			copy["partitioned"] = integer(item, 0, "partitions") > 0
			copy["internal"] = boolean(item, "internal")
			if kind == "rabbitmq" {
				copy["persistent"] = boolean(item, "durable")
				copy["partitioned"] = false
				for target, source := range map[string]string{"messageCount": "messages", "consumerCount": "consumers"} {
					if v, ok := item[source]; ok {
						copy[target] = v
					}
				}
			}
		}
		if list == "namespaces" {
			copy["tenant"] = ""
			copy["namespace"] = item["name"]
		}
		result = append(result, copy)
	}
	return result
}
func mqPage(items []any, p map[string]any) (map[string]any, error) {
	page, size := integer(p, 1, "page"), integer(p, 100, "pageSize")
	if page < 1 || size < 1 || size > 500 {
		return nil, errors.New("MQ page must be positive and pageSize 1–500")
	}
	search := strings.ToLower(str(p, "search"))
	filtered := []any{}
	for _, v := range items {
		if strings.Contains(strings.ToLower(str(mapValue(v), "name")), search) {
			filtered = append(filtered, v)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		a, b := mapValue(filtered[i]), mapValue(filtered[j])
		less := str(a, "name") < str(b, "name")
		if str(p, "sort") == "messagesReady" {
			less = integer(a, 0, "messagesReady") < integer(b, 0, "messagesReady")
		}
		if boolean(p, "sortDescending") {
			if str(p, "sort") == "messagesReady" {
				return integer(a, 0, "messagesReady") > integer(b, 0, "messagesReady")
			}
			return str(a, "name") > str(b, "name")
		}
		return less
	})
	start := int64(page-1) * int64(size)
	rows := []any{}
	end := start
	if start < int64(len(filtered)) {
		end = start + int64(size)
		if end > int64(len(filtered)) {
			end = int64(len(filtered))
		}
		rows = filtered[start:end]
	}
	return map[string]any{"items": rows, "page": page, "pageSize": size, "totalCount": len(filtered), "hasMore": end < int64(len(filtered))}, nil
}
func (d *agentDriver) handleMQ(ctx context.Context, path string, p map[string]any, locked bool) (any, error) {
	name := strings.TrimPrefix(path, "/api/mq/")
	kind := mqKind(d.config)
	if name == "test-connection" {
		return d.mqCluster(ctx)
	}
	params := map[string]any{}
	for k, v := range p {
		if k != "connectionId" {
			params[k] = v
		}
	}
	topic := mapValue(p["topic"])
	topicName := str(topic, "topic")
	if topicName == "" {
		topicName = str(p, "topic")
	}
	namespace := str(mapValue(p["ns"]), "namespace")
	if namespace == "" {
		namespace = str(topic, "namespace")
	}
	if namespace == "" {
		namespace = str(p, "virtualHost")
	}
	if namespace == "*" {
		params["all_vhosts"] = true
	} else if namespace != "" {
		params["virtual_host"] = namespace
	}
	if boolean(p, "allVhosts") {
		params["all_vhosts"] = true
	}
	params["name"], params["topic"], params["groupId"] = topicName, topicName, str(p, "sub", "groupId")
	if topicName == "" {
		params["name"] = str(p, "name")
	}
	for _, key := range []string{"brokerName", "readQueueNums", "writeQueueNums", "messageType"} {
		if topic[key] != nil {
			params[key] = topic[key]
		}
	}
	if p["partitions"] != nil {
		params["totalPartitions"] = p["partitions"]
	}
	method, list := "", ""
	readOnly := true
	switch name {
	case "topics/list", "topics/list-page":
		method, list = "mq_list_topics", "topics"
	case "topics/create":
		method = "mq_create_topic"
		readOnly = false
		params["durable"] = true
		if params["partitions"] == nil {
			params["partitions"] = 1
		}
		params["replicationFactor"] = 1
	case "topics/delete":
		method = "mq_delete_topic"
		readOnly = false
	case "topics/update-partitions":
		if kind == "rabbitmq" {
			return nil, unsupported("RabbitMQ partitions")
		}
		method = "mq_update_partitions"
		readOnly = false
	case "topics/stats", "monitoring/backlog":
		method = "mq_get_topic_stats"
	case "topics/internal-stats", "policies/effective":
		method = "mq_get_topic_config"
		if name == "policies/effective" {
			params["name"] = str(mapValue(p["scope"]), "topic")
		}
	case "topics/route":
		if kind != "rocketmq" {
			return nil, unsupported(path)
		}
		method = "mq_get_topic_route"
	case "topics/alter-config":
		method = "mq_alter_topic_config"
		readOnly = false
	case "topics/skip-accumulation":
		if kind != "rocketmq" {
			return nil, unsupported(path)
		}
		method = "mq_skip_topic_accumulation"
		readOnly = false
	case "namespaces/list":
		if kind != "rabbitmq" {
			return nil, unsupported("MQ namespaces")
		}
		method, list = "mq_list_namespaces", "namespaces"
	case "namespaces/create", "namespaces/delete":
		if kind != "rabbitmq" {
			return nil, unsupported("MQ namespaces")
		}
		params["namespace"] = namespace
		method = "mq_" + strings.Split(name, "/")[1] + "_namespace"
		readOnly = false
	case "exchanges/list", "exchanges/list-page":
		if kind != "rabbitmq" {
			return nil, unsupported(path)
		}
		method, list = "mq_list_exchanges", "exchanges"
	case "exchanges/create", "exchanges/delete":
		method = "mq_" + strings.Split(name, "/")[1] + "_exchange"
		readOnly = false
		params["type"] = p["exchangeType"]
	case "bindings/list":
		method, list = "mq_list_bindings", "bindings"
	case "bindings/bind", "bindings/unbind":
		method = "mq_" + strings.Split(name, "/")[1]
		readOnly = false
		for k, v := range mapValue(p["binding"]) {
			params[k] = v
		}
	case "client-connections/list":
		method, list = "mq_list_connections", "connections"
	case "channels/list":
		method, list = "mq_list_channels", "channels"
	case "client-connections/close":
		method = "mq_close_connection"
		readOnly = false
	case "users/list":
		method, list = "mq_list_users", "users"
	case "users/create", "users/delete":
		method = "mq_" + strings.Split(name, "/")[1] + "_user"
		readOnly = false
	case "user-permissions/list":
		method, list = "mq_list_permissions", "permissions"
	case "user-permissions/grant", "user-permissions/revoke":
		method = "mq_" + strings.Split(name, "/")[1] + "_permission"
		readOnly = false
	case "policies/list":
		method, list = "mq_list_policies", "policies"
	case "policies/set", "policies/delete":
		method = "mq_" + strings.Split(name, "/")[1] + "_policy"
		readOnly = false
	case "overview":
		method = "mq_overview"
	case "nodes":
		method, list = "mq_list_nodes", "nodes"
	case "monitoring/cluster-info":
		method = "mq_describe_cluster"
	case "kafka/consumer-groups":
		if kind != "kafka" {
			return nil, unsupported(path)
		}
		method = "mq_get_consumer_group_snapshot"
		params["timeout_ms"] = mqAgentConnectParams(d.config)["request_timeout_ms"]
	case "subscriptions/list", "subscriptions/enrich":
		if kind == "rabbitmq" {
			method, list = "mq_list_consumers", "consumers"
		} else {
			method, list = "mq_list_consumer_groups", "groups"
			params["enrich"] = name == "subscriptions/enrich"
			params["includeLag"] = topicName != ""
		}
	case "subscriptions/delete":
		if kind == "rabbitmq" {
			return nil, unsupported("RabbitMQ consumer deletion")
		}
		method = "mq_delete_consumer_group"
		readOnly = false
	case "subscriptions/reset-cursor", "subscriptions/clear-backlog":
		readOnly = false
		if kind == "rabbitmq" {
			if name != "subscriptions/clear-backlog" {
				return nil, unsupported(path)
			}
			method = "mq_purge_queue"
		} else {
			method = "mq_reset_consumer_group_offsets"
			if name == "subscriptions/clear-backlog" {
				params["position"] = "latest"
			} else {
				pos := p["pos"]
				if value, ok := pos.(string); ok {
					params["position"] = value
				} else {
					for k, v := range mapValue(pos) {
						params[k] = v
					}
					params["position"] = str(mapValue(pos), "kind")
					if params["position"] == "partitionOffset" {
						if kind != "kafka" {
							return nil, unsupported("RocketMQ partition cursor reset")
						}
						partition, offset := integer(mapValue(pos), -1, "partition"), integer(mapValue(pos), -1, "offset")
						if partition < 0 || offset < 0 {
							return nil, errors.New("partition and offset must be non-negative")
						}
						params["offsets"] = []any{map[string]any{"partition": partition, "offset": offset}}
						delete(params, "position")
					}
					if params["position"] == "messageId" {
						return nil, unsupported("MQ agent message-id cursor reset")
					}
				}
			}
		}
	case "subscriptions/peek-messages":
		method = "mq_peek_messages"
		for k, v := range mapValue(p["options"]) {
			params[k] = v
		}
		params["connection"] = mqAgentConnectParams(d.config)
		count := integer(p, 10, "count")
		if count < 1 || count > 1000 {
			return nil, errors.New("MQ peek count must be 1–1000")
		}
		params["count"] = count
	case "producers/list":
		method, list = "mq_list_producers", "producers"
	case "consumers/list":
		method, list = "mq_describe_consumer_group", "members"
		if kind == "rabbitmq" {
			method, list = "mq_list_consumers", "consumers"
		}
	case "consumers/group-config/get", "consumers/group-config/alter":
		if kind != "rocketmq" {
			return nil, unsupported(path)
		}
		method = "mq_get_subscription_group_config"
		if strings.HasSuffix(name, "/alter") {
			method = "mq_alter_subscription_group_config"
			readOnly = false
			for k, v := range mapValue(p["config"]) {
				params[k] = v
			}
		}
	case "messages/view", "messages/query-by-key", "messages/query-by-topic", "messages/trace":
		if kind != "rocketmq" {
			return nil, unsupported(path)
		}
		method = map[string]string{"messages/view": "mq_view_message", "messages/query-by-key": "mq_query_message_by_key", "messages/query-by-topic": "mq_query_message_by_topic", "messages/trace": "mq_query_message_trace"}[name]
	case "send-message":
		method = "mq_send_message"
		readOnly = false
		params = mapValue(p["req"])
		if kind == "rabbitmq" && str(params, "namespace") != "" {
			params["virtual_host"] = params["namespace"]
		}
	case "policies/retention":
		if kind != "kafka" {
			return nil, unsupported(path)
		}
		readOnly = false
		method = "mq_alter_topic_config"
		scope, retention := mapValue(p["scope"]), mapValue(p["retention"])
		if str(scope, "topic") == "" {
			return nil, errors.New("retention requires a topic")
		}
		params["name"] = scope["topic"]
		minutes := integer(retention, 0, "retentionTimeInMinutes")
		milliseconds := int64(minutes) * 60000
		if minutes < 0 {
			milliseconds = -1
		}
		configs := []any{map[string]any{"key": "retention.ms", "value": strconv.FormatInt(milliseconds, 10)}}
		if mb := integer(retention, -1, "retentionSizeInMb"); mb >= 0 && kind == "kafka" {
			configs = append(configs, map[string]any{"key": "retention.bytes", "value": strconv.FormatInt(int64(mb)*(1<<20), 10)})
		}
		params["configs"] = configs
	default:
		return nil, unsupported(path)
	}
	// List-only all-vhost context must never target a default vhost mutation.
	if !readOnly && (locked || params["all_vhosts"] == true || str(params, "virtual_host") == "*") {
		if locked {
			return nil, errors.New("writes are locked; unlock writes before changing the message queue")
		}
		return nil, errors.New("mutation requires a specific virtual host")
	}
	if kind != "rabbitmq" && (strings.HasPrefix(name, "exchanges/") || strings.HasPrefix(name, "bindings/") || strings.HasPrefix(name, "users/") || strings.HasPrefix(name, "user-permissions/") || strings.HasPrefix(name, "client-connections/") || strings.HasPrefix(name, "channels/") || name == "overview" || name == "nodes" || strings.HasPrefix(name, "policies/set") || strings.HasPrefix(name, "policies/list") || strings.HasPrefix(name, "policies/delete")) {
		return nil, unsupported(path)
	}
	var result any
	if err := d.session.Call(ctx, method, params, &result); err != nil {
		return nil, err
	}
	if list != "" {
		items, err := mqEnvelope(result, list)
		if err != nil {
			return nil, err
		}
		// RocketMQ agents always bound topic and group lists. Drain subsequent
		// pages so legacy full-list routes do not silently truncate at 200 rows.
		if kind == "rocketmq" && (list == "topics" || list == "groups") {
			for len(items) < integer(mapValue(result), len(items), "total") {
				if len(items) > 100000 {
					return nil, errors.New("MQ list exceeds 100000 entries")
				}
				params["offset"], params["limit"] = len(items), 200
				var next any
				if err := d.session.Call(ctx, method, params, &next); err != nil {
					return nil, err
				}
				batch, err := mqEnvelope(next, list)
				if err != nil {
					return nil, err
				}
				if len(batch) == 0 {
					return nil, errors.New("MQ pagination ended before the reported total")
				}
				items = append(items, batch...)
			}
		}
		items = normalizeMQItems(items, kind, list)
		if strings.HasSuffix(name, "-page") {
			return mqPage(items, mapValue(p["pagination"]))
		}
		if strings.HasPrefix(name, "subscriptions/") {
			return mqSubscriptions(items, kind, topicName), nil
		}
		if name == "consumers/list" {
			return mqConsumers(items), nil
		}
		return items, nil
	}
	if name == "subscriptions/peek-messages" {
		return mqPeekResult(result, kind)
	}
	if name == "topics/stats" {
		return mqTopicStats(mapValue(result), kind), nil
	}
	if name == "monitoring/backlog" {
		return nil, unsupported("MQ backlog aggregation; use topic statistics")
	}
	return result, nil
}
func mqConsumers(items []any) []any {
	result := []any{}
	for _, item := range items {
		m := mapValue(item)
		result = append(result, map[string]any{"consumerName": str(m, "memberId", "tag", "name"), "msgRateOut": 0, "msgThroughputOut": 0, "availablePermits": integer(m, 0, "prefetch"), "address": str(m, "host", "name"), "clientVersion": str(m, "clientVersion")})
	}
	return result
}
func mqSubscriptions(items []any, kind, topic string) []any {
	result := []any{}
	for _, item := range items {
		m := mapValue(item)
		name := str(m, "groupId", "tag", "name", "consumerTag")
		consumers := anySlice(m["members"])
		backlog := int64(integer(m, 0, "totalLag"))
		unavailable := m["totalLag"] == nil || boolean(m, "totalLagFailed")
		if kind == "rabbitmq" {
			consumers = []any{m}
			backlog = 0
			unavailable = true
		}
		if kind == "kafka" && topic != "" {
			assigned := []any{}
			for _, member := range consumers {
				for _, assignment := range anySlice(mapValue(member)["assignments"]) {
					if str(mapValue(assignment), "topic") == topic {
						assigned = append(assigned, member)
						break
					}
				}
			}
			consumers = assigned
			ends := map[int]int64{}
			for _, offset := range anySlice(m["endOffsets"]) {
				o := mapValue(offset)
				if str(o, "topic") == topic {
					ends[integer(o, -1, "partition")] = int64(integer(o, -1, "offset"))
				}
			}
			committed := false
			backlog = 0
			unavailable = false
			for _, offset := range anySlice(m["committedOffsets"]) {
				o := mapValue(offset)
				if str(o, "topic") != topic {
					continue
				}
				committed = true
				end, ok := ends[integer(o, -1, "partition")]
				value := int64(integer(o, -1, "offset"))
				if !ok || value < 0 {
					unavailable = true
					continue
				}
				if end > value {
					backlog += end - value
				}
			}
			if !committed && len(consumers) == 0 {
				continue
			}
		}
		typeName := "consumer-group"
		if kind == "rabbitmq" {
			typeName = "queue-consumer"
		}
		entry := map[string]any{"name": name, "subType": typeName, "msgBacklog": backlog, "backlogUnavailable": unavailable, "msgRateOut": 0, "msgThroughputOut": 0, "consumers": mqConsumers(consumers), "raw": m}
		for target, source := range map[string]string{"topics": "topics", "onlineMembers": "onlineMembers", "consumerGroupType": "groupType", "messageModel": "messageModel"} {
			if v, ok := m[source]; ok {
				entry[target] = v
			}
		}
		result = append(result, entry)
	}
	return result
}
func mqPeekResult(raw any, kind string) (any, error) {
	items, err := mqEnvelope(raw, "messages")
	if err != nil {
		return nil, err
	}
	result := []any{}
	for i, v := range items {
		m := mapValue(v)
		out := map[string]any{}
		for k, v := range m {
			out[k] = v
		}
		out["position"] = i + 1
		if kind == "kafka" {
			out["messageId"] = valueString(m["offset"])
			out["publishTime"] = valueString(m["timestamp"])
			out["properties"] = map[string]any{"partition": valueString(m["partition"])}
		} else {
			if out["messageId"] == nil {
				out["messageId"] = valueString(m["deliveryTag"])
			}
			if out["publishTime"] == nil && integer(m, 0, "timestamp") != 0 {
				out["publishTime"] = valueString(m["timestamp"])
			}
		}
		result = append(result, out)
	}
	return map[string]any{"messages": result, "incomplete": boolean(mapValue(raw), "incomplete")}, nil
}
func mqTopicStats(raw map[string]any, kind string) map[string]any {
	out := map[string]any{"raw": raw, "ratesUnavailable": true}
	for _, key := range []string{"msgRateIn", "msgRateOut", "msgThroughputIn", "msgThroughputOut", "storageSize", "backlogSize", "msgInCounter", "msgOutCounter", "subscriptionCount", "producerCount"} {
		out[key] = 0
	}
	if kind != "rabbitmq" {
		if v, ok := raw["totalMessages"]; ok {
			out["msgInCounter"] = v
		}
	} else {
		for target, source := range map[string]string{"msgRateIn": "publishRate", "msgRateOut": "deliverRate", "backlogSize": "totalMessages", "msgInCounter": "publishTotal", "msgOutCounter": "deliverGetTotal", "subscriptionCount": "consumerCount"} {
			if v, ok := raw[source]; ok {
				out[target] = v
			}
		}
		out["ratesUnavailable"] = raw["publishRate"] == nil && raw["deliverRate"] == nil && raw["ackRate"] == nil
	}
	return out
}
