import type { ObjectSourceKind } from "@/types/database";

export interface ConnectionContentSearchRequest {
  connection_id: string;
  query: string;
  limit?: number;
  exclude_ids?: string[];
  /** Return one hit per object/query when listing sources alongside name matches. */
  first_match_only?: boolean;
}

export interface ObjectContentSearchRequest extends ConnectionContentSearchRequest {
  database?: string;
  schema?: string;
  databases?: string[];
  schemas?: string[] | null;
}

/** Positions are UTF-16 offsets into the editor text, including supplementary characters. */
export interface ContentSearchLocation {
  line: number;
  column: number;
  line_text: string;
  match_text: string;
  start: number;
  end: number;
}

export interface ObjectContentSearchMatch extends ContentSearchLocation {
  object_type: ObjectSourceKind;
  object_name: string;
  database: string;
  schema: string;
  signature?: string;
}

export interface SavedQueryContentSearchMatch extends ContentSearchLocation {
  id: string;
  name: string;
  connection_id: string;
  database: string;
  catalog?: string;
  schema?: string;
}

export interface ContentSearchResult<T> {
  matches: T[];
  truncated: boolean;
  warnings?: string[];
}
