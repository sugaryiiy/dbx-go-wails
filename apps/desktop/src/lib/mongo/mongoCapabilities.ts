import type { ConnectionConfig } from "@/types/database";
import { connectionIsEffectivelyReadOnly } from "@/lib/database/readOnlyWriteAccess";
import { isGoBackend } from "@/lib/backend/goCapabilities";

/** Import/export jobs, GridFS, cloning and administration are still being migrated. */
export function supportsMongoAdvancedOperations(): boolean {
  return !isGoBackend();
}

export function mongoAdvancedUnavailableMessage(locale: string): string {
  return locale.startsWith("zh") ? "Go 版暂不支持 MongoDB 文件导入、备份恢复、GridFS 和集合克隆。" : "MongoDB file import, backup/restore, GridFS and collection cloning are not available in the Go version yet.";
}

type MongoConnectionProfile = Pick<ConnectionConfig, "db_type" | "driver_profile" | "read_only"> & Partial<Pick<ConnectionConfig, "id">>;

/** Accept historical Legacy profile spellings while persisted configs are normalized. */
export function isMongoLegacyDriverProfile(driverProfile?: string): boolean {
  const profile = driverProfile?.trim().toLowerCase();
  return profile === "mongodb-legacy" || profile === "mongodb_legacy" || profile === "legacy";
}

/** Capabilities implemented by both the native driver and the Legacy Agent. */
export function supportsMongoAllDriverMutations(connection?: MongoConnectionProfile): boolean {
  return connection?.db_type === "mongodb" && !connectionIsEffectivelyReadOnly(connection);
}

/** MongoDB views do not own indexes; collections and time-series collections do. */
export function mongoCollectionSupportsIndexes(collectionKind?: string): boolean {
  return collectionKind?.trim().toLowerCase() !== "view";
}

export function supportsMongoIndexMutations(connection?: MongoConnectionProfile, collectionKind?: string): boolean {
  return supportsMongoAllDriverMutations(connection) && mongoCollectionSupportsIndexes(collectionKind);
}
