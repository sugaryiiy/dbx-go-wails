import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ConnectionConfig, MongoCollectionKind, TreeNode } from "@/types/database";

function installLocalStorage() {
  const data = new Map<string, string>();
  vi.stubGlobal("localStorage", {
    getItem: vi.fn((key: string) => data.get(key) ?? null),
    setItem: vi.fn((key: string, value: string) => data.set(key, value)),
    removeItem: vi.fn((key: string) => data.delete(key)),
  });
}

function mongoConnection(): ConnectionConfig {
  return {
    id: "mongo-1",
    name: "MongoDB",
    db_type: "mongodb",
    host: "127.0.0.1",
    port: 27017,
    username: "",
    password: "",
    database: "app",
  } as ConnectionConfig;
}

function findNode(nodes: TreeNode[], id: string): TreeNode | undefined {
  for (const node of nodes) {
    if (node.id === id) return node;
    const child = node.children && findNode(node.children, id);
    if (child) return child;
  }
  return undefined;
}

async function setup(kind: MongoCollectionKind) {
  const api = {
    mongoListCollections: vi.fn().mockResolvedValue([{ name: "reports", kind: "collection" }]),
    listIndexes: vi.fn().mockResolvedValue([{ name: "email_1", columns: ["email"], is_primary: false, is_unique: false }]),
  };
  vi.doMock("@/lib/backend/tauriRuntime", () => ({ isTauriRuntime: () => false }));
  vi.doMock("@/lib/backend/api", () => api);

  const { useConnectionStore } = await import("@/stores/connectionStore");
  const store = useConnectionStore();
  const config = mongoConnection();
  const collectionId = `${config.id}:app:reports`;
  store.connections = [config];
  store.connectedIds.add(config.id);
  store.treeNodes = [
    {
      id: config.id,
      label: config.name,
      type: "connection",
      connectionId: config.id,
      isExpanded: true,
      children: [
        {
          id: collectionId,
          label: "reports",
          type: "mongo-collection",
          connectionId: config.id,
          database: "app",
          meta: { collectionKind: kind },
          isExpanded: false,
          children: [],
        },
      ],
    },
  ];
  return { api, config, store, collectionId };
}

describe("connectionStore MongoDB collection groups", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.unstubAllGlobals();
    installLocalStorage();
    setActivePinia(createPinia());
    vi.doMock("@/lib/backend/goCapabilities", () => ({ isGoBackend: () => false }));
  });

  it("does not create the unsupported GridFS root in Go mode", async () => {
    vi.doMock("@/lib/backend/goCapabilities", () => ({ isGoBackend: () => true }));
    const { config, store } = await setup("collection");
    store.treeNodes = [{ id: `${config.id}:app`, label: "app", type: "mongo-db", connectionId: config.id, database: "app", isExpanded: false, children: [] }];
    await store.loadMongoCollections(config.id, "app");
    expect(store.treeNodes[0]?.children?.map((node) => node.type)).toEqual(["mongo-collection"]);
  });

  it("does not expose an Indexes group or issue listIndexes for a view", async () => {
    const { api, config, store, collectionId } = await setup("view");

    await store.loadTableGroups(config.id, "app", "reports", undefined, collectionId);

    const collection = findNode(store.treeNodes, collectionId)!;
    expect(collection.children?.some((node) => node.type === "group-indexes")).toBe(false);

    // A stale tree node from an earlier metadata load must also remain safe.
    const indexGroup: TreeNode = {
      id: `${collectionId}:__indexes`,
      label: "tree.indexes",
      type: "group-indexes",
      connectionId: config.id,
      database: "app",
      tableName: "reports",
      meta: { collectionKind: "view" },
      isExpanded: false,
      children: [],
    };
    collection.children = [indexGroup];

    await store.loadIndexes(config.id, "app", "reports", undefined, indexGroup.id);

    expect(api.listIndexes).not.toHaveBeenCalled();
    expect(findNode(store.treeNodes, indexGroup.id)).toMatchObject({ children: [], isExpanded: true });
  });

  it("keeps the Indexes group available for ordinary collections", async () => {
    const { api, config, store, collectionId } = await setup("collection");

    await store.loadTableGroups(config.id, "app", "reports", undefined, collectionId);

    const indexGroup = findNode(store.treeNodes, `${collectionId}:__indexes`)!;
    expect(indexGroup.type).toBe("group-indexes");
    await store.loadIndexes(config.id, "app", "reports", undefined, indexGroup.id);

    expect(api.listIndexes).toHaveBeenCalledOnce();
    expect(indexGroup.children?.[0]).toMatchObject({ type: "index", label: "email_1 (email)" });
  });
});
