// MQTT HTTP backend stubs (MQTT is not yet supported in web/Docker mode).
// Throw descriptive errors so the user knows this isn't available.
// Return types mirror the shared backend surface so api consumers keep
// the same static types as the desktop transport had.

import type { MqttBrokerInfo, MqttMessage, MqttPublishRequest, MqttSavedTopic, MqttTopicNode } from "@/types/mqtt";

function mqttWebNotAvailable(name: string): never {
  throw new Error(`MQTT ${name}: MQTT 功能暂不支持 Web/Docker 模式。请使用桌面版。`);
}

export async function mqttGetBrokerInfo(_connectionId: string): Promise<MqttBrokerInfo> {
  return mqttWebNotAvailable("mqttGetBrokerInfo");
}

export async function mqttSubscribe(_connectionId: string, _topic: string, _qos?: string | null, _noLocal?: boolean): Promise<void> {
  return mqttWebNotAvailable("mqttSubscribe");
}

export async function mqttSaveTopicConfig(_connectionId: string, _topic: string, _qos: string, _noLocal: boolean, _enabled = false): Promise<void> {
  return mqttWebNotAvailable("mqttSaveTopicConfig");
}

export async function mqttDeleteTopicConfig(_connectionId: string, _topic: string): Promise<void> {
  return mqttWebNotAvailable("mqttDeleteTopicConfig");
}

export async function mqttUnsubscribe(_connectionId: string, _topic: string): Promise<void> {
  return mqttWebNotAvailable("mqttUnsubscribe");
}

export async function mqttPublish(_connectionId: string, _request: MqttPublishRequest): Promise<void> {
  return mqttWebNotAvailable("mqttPublish");
}

export async function mqttListTopics(_connectionId: string): Promise<[string, string][]> {
  return mqttWebNotAvailable("mqttListTopics");
}

export async function mqttListSavedTopicConfigs(_connectionId: string): Promise<MqttSavedTopic[]> {
  return mqttWebNotAvailable("mqttListSavedTopicConfigs");
}

export async function mqttGetTopicTree(_connectionId: string): Promise<MqttTopicNode> {
  return mqttWebNotAvailable("mqttGetTopicTree");
}

export async function mqttGetMessages(_connectionId: string, _topicFilter?: string | null, _limit?: number | null): Promise<MqttMessage[]> {
  return mqttWebNotAvailable("mqttGetMessages");
}

export async function mqttClearMessages(_connectionId: string): Promise<void> {
  return mqttWebNotAvailable("mqttClearMessages");
}
