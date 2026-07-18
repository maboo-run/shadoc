export type ControlPlaneImportConflict = {
  resourceType: string;
  resourceId?: string;
  field: string;
  value?: string;
  existingId?: string;
};

export type ControlPlaneMissingTool = {
  tool: string;
  path?: string;
  requiredBy: string[];
};

export type ControlPlaneRevalidationItem = {
  resourceType: string;
  resourceId: string;
  action: string;
};

export type ControlPlaneImportPreview = {
  previewId?: string;
  expiresAt?: string;
  canImport: boolean;
  sourceApplicationVersion: string;
  resourceCounts: Record<string, number>;
  conflicts: ControlPlaneImportConflict[];
  missingTools: ControlPlaneMissingTool[];
  revalidation: ControlPlaneRevalidationItem[];
  excludedTransientClasses: string[];
  restartRequired: boolean;
  warnings: string[];
};

export type ControlPlaneRecoveryDownload = {
  blob: Blob;
  filename: string;
};

export type ControlPlaneExportRequest = {
  administratorPassword: string;
  recoveryPassphrase: string;
  recoveryPassphraseConfirmation: string;
};

export type ControlPlaneImportRequest = {
  recoveryPassphrase: string;
  previewId: string;
  administratorPassword: string;
  impactConfirmed: boolean;
};
