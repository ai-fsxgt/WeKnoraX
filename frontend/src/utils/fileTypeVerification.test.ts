import assert from 'node:assert/strict'
import test from 'node:test'

import { getAllowedKnowledgeFileTypes, shouldRejectKnowledgeFileType } from './fileTypeVerification.ts'

test('shouldRejectKnowledgeFileType accepts HTML in the fallback whitelist', () => {
  assert.equal(shouldRejectKnowledgeFileType('page.html'), false)
  assert.equal(shouldRejectKnowledgeFileType('legacy.HTM'), false)
  assert.equal(shouldRejectKnowledgeFileType('payload.exe'), true)
  const allowed = getAllowedKnowledgeFileTypes()
  assert.ok(allowed.includes('html'))
  assert.deepEqual(getAllowedKnowledgeFileTypes([]), allowed)
  assert.deepEqual(getAllowedKnowledgeFileTypes(new Set()), allowed)
  for (const type of ['xlsx', 'xls', 'csv']) {
    assert.equal(allowed.includes(type), false)
    assert.equal(shouldRejectKnowledgeFileType(`table.${type}`), true)
  }
})

test('shouldRejectKnowledgeFileType preserves dynamic whitelist behavior', () => {
  assert.equal(shouldRejectKnowledgeFileType('custom.xyz', ['xyz']), false)
  assert.equal(shouldRejectKnowledgeFileType('page.html', ['pdf']), true)
  assert.equal(shouldRejectKnowledgeFileType('page.html', []), false)
  const types = ['pdf', 'xlsx', 'xls', 'csv', 'xyz']
  const validTypes = new Set(types)
  assert.deepEqual(getAllowedKnowledgeFileTypes(types), ['pdf', 'xyz'])
  assert.deepEqual(getAllowedKnowledgeFileTypes(validTypes), ['pdf', 'xyz'])
  assert.deepEqual([...validTypes], types)
  for (const type of ['xlsx', 'xls', 'csv']) {
    assert.equal(shouldRejectKnowledgeFileType(`table.${type}`, validTypes), true)
    assert.equal(shouldRejectKnowledgeFileType(`table.${type.toUpperCase()}`, validTypes), true)
  }
})
