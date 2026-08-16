"""Building blocks for the blog source extractor.

Modules, in the order the pipeline uses them:

  progress     stderr logging and a progress counter
  models       the Candidate record passed between stages
  urls         link cleaning, collapsing a link to the blog it belongs to
  tags         the tag vocabulary and how tags are inferred
  naming       reading a title, description and feed links out of an HTML head
  sourcelists  reading the GitHub blog lists and pulling links out of them
  checks       reachability and feed discovery for one candidate
  output       ids, validation and writing blogs.yml plus the audit CSV
"""
