// Author / Agent: Codex, 2026-11-15
// Shared by the live runner and saved-evidence verifier.
export function checkCollab(tag, r) {
  if (!r) return false;
  const stress = tag.startsWith('S1 ');
  const conflicts = stress && r.allDeleteConflictsExplained && r.allNackCodes.every(c => c === 'cell_gone' || c === 'terminal_gone' || c === 'duplicate_cell');
  const semantic = stress ? r.recoveryMarkers && r.recoveryConverged
    : tag.startsWith('S2 ') ? r.allRestored && r.childrenParentOK && r.bobEditsIntact
    : tag.startsWith('S3 ') ? r.midConv && r.afterUndo.length === 3 && r.afterUndo.every(x => x === 'X=- Y=-') && r.afterRedo.length === 3 && r.afterRedo.every(x => x === 'X=X2/#ff0000 Y=Y2/#0000ff')
    : tag.startsWith('S4 ') ? r.page2RestoredAll && r.uiPagesAll === '2,2,2' && r.bobEditsIntact && r.conv1
    : tag.startsWith('S5 ') ? !r.undoRedo.includes('(noop)') && r.gapInfo.rejoined && r.gapInfo.convAfterReconnect && (r.kind !== 'offline' || r.gapInfo.offlineSeen && r.gapInfo.offlineEditSurvived === 'true,true')
    : false;
  const pass = semantic && r.conv && r.conv.ok && (r.serverNacks === 0 || conflicts) && r.clientNacked === r.serverNacks && r.throws === 0 && r.mismatch === 0 && r.hashBad === 0 && r.pageErrors === 0;


  return !!pass;
}
