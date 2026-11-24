import assert from 'node:assert/strict'
import test from 'node:test'
import { looksLikeMarkdown, markdownTitle, stripMarkdown, withoutTitle } from '../src/lib/memory-text'

const brief = '# Project brief: checkout-service\nWhat it is: the **Go** service.\nStack: `Postgres 16`.\n\n## Key decisions\n- Retry [429](https://x.dev) with backoff.\n- Money is integer minor units.'

test('markdown memories are detected; plain sentences are not', () => {
  assert.equal(looksLikeMarkdown(brief), true)
  assert.equal(looksLikeMarkdown('Captures retry 429 and 5xx responses, capped at 2 s.'), false)
  assert.equal(looksLikeMarkdown('Uses snake_case for column names and 2*3 grids.'), false)
})

test('the leading heading becomes the title and leaves the body', () => {
  assert.equal(markdownTitle(brief), 'Project brief: checkout-service')
  assert.ok(withoutTitle(brief).startsWith('What it is:'))
  assert.equal(markdownTitle('Plain text'), '')
  assert.equal(withoutTitle('Plain text'), 'Plain text')
})

test('previews strip markdown syntax', () => {
  assert.equal(
    stripMarkdown(withoutTitle(brief)),
    'What it is: the Go service. Stack: Postgres 16. Key decisions: Retry 429 with backoff. Money is integer minor units.',
  )
  assert.equal(stripMarkdown('snake_case_name stays'), 'snake_case_name stays')
})
