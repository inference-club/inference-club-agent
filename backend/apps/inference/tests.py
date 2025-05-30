from django.test import TestCase
from django.urls import reverse
from rest_framework.test import APITestCase
from rest_framework import status
from apps.services.models import TTSService

from .models import InferenceRequest

class InferenceRequestTTSServiceFilterTests(APITestCase):
    def setUp(self):
        self.tts_service1 = TTSService.objects.create(slug='tts1', url='http://localhost:1234', type='Dia')
        self.tts_service2 = TTSService.objects.create(slug='tts2', url='http://localhost:5678', type='ChatTTS')
        self.req1 = InferenceRequest.objects.create(
            inference_type='tts',
            payload={'text_input': 'hello'},
            tts_service=self.tts_service1,
            status='completed'
        )
        self.req2 = InferenceRequest.objects.create(
            inference_type='tts',
            payload={'text_input': 'world'},
            tts_service=self.tts_service2,
            status='completed'
        )
        self.req3 = InferenceRequest.objects.create(
            inference_type='tts',
            payload={'text_input': 'foo'},
            tts_service=self.tts_service1,
            status='completed'
        )

    def test_filter_by_tts_service(self):
        url = reverse('inferencerequest-list')
        response = self.client.get(url, {'tts_service': self.tts_service1.id})
        self.assertEqual(response.status_code, status.HTTP_200_OK)
        ids = [r['id'] for r in response.data]
        self.assertIn(self.req1.id, ids)
        self.assertIn(self.req3.id, ids)
        self.assertNotIn(self.req2.id, ids)
        self.assertEqual(len(ids), 2)

    def test_filter_by_tts_service_no_results(self):
        # Use a non-existent service id
        url = reverse('inferencerequest-list')
        response = self.client.get(url, {'tts_service': 9999})
        self.assertEqual(response.status_code, status.HTTP_200_OK)
        self.assertEqual(len(response.data), 0)